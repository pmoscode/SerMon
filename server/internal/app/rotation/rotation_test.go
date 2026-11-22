package rotation_test

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now  = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	org  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	node = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
	id   = rotation.Identity{Org: org, Node: node, Serial: "old"}
)

type fakeSigner struct{ err error }

func (f fakeSigner) SignAgentCertificate(o tenant.OrgID, _ []byte, n identity.NodeID) (pki.Issued, agentca.CertificateRecord, error) {
	if f.err != nil {
		return pki.Issued{}, agentca.CertificateRecord{}, f.err
	}
	return pki.Issued{DER: []byte("new"), Serial: "new", NotAfter: now.Add(30 * 24 * time.Hour), Intermediate: []byte("intermediate"), Root: []byte("root")},
		agentca.CertificateRecord{Serial: "new", Org: o, Node: n}, nil
}

// Intermediate is the CA issuing *now*: after a rollover between signing and answering it is no
// longer the one that signed (the rotation must not use it, M1-38).
func (fakeSigner) Intermediate() *x509.Certificate {
	return &x509.Certificate{Raw: []byte("intermediate-after-rollover")}
}

type fakeStore struct {
	newAddress []string // NoteNewAddress calls (M2-24)
	rotateErr  error
	revokeOld  time.Time
	next       agentca.CertificateRecord
	locked     string
	audits     []access.AuditEntry
	previousIP string
}

func (f *fakeStore) Rotate(_ context.Context, _ rotation.Identity, next agentca.CertificateRecord, revokeOldAt time.Time, a access.AuditEntry) error {
	f.next, f.revokeOld = next, revokeOldAt
	f.audits = append(f.audits, a)
	return f.rotateErr
}

func (f *fakeStore) Lock(_ context.Context, _ tenant.OrgID, _ identity.NodeID, reason string, _ time.Time, a access.AuditEntry) error {
	f.locked = reason
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) RecordSession(_ context.Context, _ tenant.OrgID, _ identity.NodeID, _ string) (string, error) {
	return f.previousIP, nil
}

func (f *fakeStore) Note(_ context.Context, _ tenant.OrgID, a access.AuditEntry) error {
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) NoteNewAddress(_ context.Context, _ tenant.OrgID, node identity.NodeID, ip, previous string, _ time.Time, a access.AuditEntry) error {
	f.audits = append(f.audits, a)
	f.newAddress = append(f.newAddress, node.String()+" "+previous+"->"+ip)
	return nil
}

func service(store *fakeStore, signer fakeSigner) *rotation.Service {
	return rotation.NewService(store, signer, func() time.Time { return now })
}

func TestRotate_IssuesForTheSameIdentityAndKeepsTheOldCertificateForTheGracePeriod(t *testing.T) {
	store := &fakeStore{}
	res, err := service(store, fakeSigner{}).Rotate(context.Background(), id, []byte("csr"), "192.0.2.1", "192.0.2.1", false)
	if err != nil {
		t.Fatal(err)
	}
	if store.next.Org != org || store.next.Node != node || store.next.Serial != "new" {
		t.Errorf("new certificate = %+v, want the session's identity", store.next)
	}
	if want := now.Add(rotation.Grace); !store.revokeOld.Equal(want) || !res.PreviousValidUntil.Equal(want) {
		t.Errorf("old certificate valid until %v / %v, want %v", store.revokeOld, res.PreviousValidUntil, want)
	}
	if string(res.Certificate) != "new" || res.Serial != "new" || string(res.Intermediate) != "intermediate" || store.audits[0].Action != "certificate.rotate" || len(store.audits) != 1 {
		t.Errorf("result %+v, audits %+v", res, store.audits)
	}
}

func TestRotate_WithAReplacedCertificateLocksTheNode(t *testing.T) {
	store := &fakeStore{rotateErr: fmt.Errorf("store: %w", rotation.ErrSuperseded)}
	_, err := service(store, fakeSigner{}).Rotate(context.Background(), id, []byte("csr"), "192.0.2.1", "192.0.2.1", false)
	if !errors.Is(err, rotation.ErrLocked) {
		t.Fatalf("error = %v, want ErrLocked", err)
	}
	if store.locked == "" || store.audits[len(store.audits)-1].Action != "security.node_locked" {
		t.Errorf("node not locked: reason %q, audits %+v", store.locked, store.audits)
	}
}

func TestRotate_FromANewSourceIPLeavesASecurityNote(t *testing.T) {
	store := &fakeStore{}
	if _, err := service(store, fakeSigner{}).Rotate(context.Background(), id, []byte("csr"), "198.51.100.7", "192.0.2.1", false); err != nil {
		t.Fatal(err)
	}
	if len(store.newAddress) != 1 || !strings.HasSuffix(store.newAddress[0], "192.0.2.1->198.51.100.7") {
		t.Errorf("new address recorded as %v, want the security event with both addresses", store.newAddress)
	}
	if len(store.audits) != 2 || store.audits[1].Action != "security.rotation_new_ip" || store.audits[1].Details["previous_ip"] != "192.0.2.1" {
		t.Errorf("audits = %+v", store.audits)
	}
}

func TestRotate_RejectsBadCSRs(t *testing.T) {
	store := &fakeStore{}
	_, err := service(store, fakeSigner{err: fmt.Errorf("x: %w", pki.ErrInvalidCSR)}).Rotate(context.Background(), id, []byte("bad"), "192.0.2.1", "", false)
	if !errors.Is(err, rotation.ErrInvalidCSR) || len(store.audits) != 0 {
		t.Errorf("error = %v, audits %+v", err, store.audits)
	}
}

func TestLock_RevokesAndAudits(t *testing.T) {
	store := &fakeStore{}
	if err := service(store, fakeSigner{}).Lock(context.Background(), id, "second session from 198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if store.locked != "second session from 198.51.100.7" || store.audits[0].Action != "security.node_locked" || !store.audits[0].Actor.IsZero() {
		t.Errorf("locked %q, audits %+v", store.locked, store.audits)
	}
}

func TestSessionOpened_ReturnsThePreviousIP(t *testing.T) {
	prev, err := service(&fakeStore{previousIP: "192.0.2.1"}, fakeSigner{}).SessionOpened(context.Background(), id, "192.0.2.9")
	if err != nil || prev != "192.0.2.1" {
		t.Errorf("SessionOpened() = %q, %v", prev, err)
	}
}

func TestRotate_ImmediateRevokesThePreviousCertificateAtOnce(t *testing.T) {
	store := &fakeStore{}
	res, err := service(store, fakeSigner{}).Rotate(context.Background(), id, []byte("csr"), "192.0.2.1", "", true)
	if err != nil || !store.revokeOld.Equal(now) || !res.PreviousValidUntil.Equal(now) {
		t.Errorf("revoke old at %v (%v), want now", store.revokeOld, err)
	}
}
