package agentca_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

var (
	t0   = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	orgA = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	node = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
)

// fakeStore is an in-memory agentca.Store.
type fakeStore struct {
	authority *agentca.AuthorityRecord
	certs     map[string]agentca.CertificateRecord
	revoked   map[string]time.Time
	inserts   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{certs: map[string]agentca.CertificateRecord{}, revoked: map[string]time.Time{}}
}

func (f *fakeStore) ActiveAuthority(context.Context) (agentca.AuthorityRecord, bool, error) {
	if f.authority == nil {
		return agentca.AuthorityRecord{}, false, nil
	}
	return *f.authority, true, nil
}

func (f *fakeStore) InsertAuthority(_ context.Context, r agentca.AuthorityRecord) error {
	f.inserts++
	if f.authority != nil {
		return agentca.ErrAuthorityExists
	}
	f.authority = &r
	return nil
}

func (f *fakeStore) RecordCertificate(_ context.Context, scope tenant.Scope, c agentca.CertificateRecord) error {
	if c.Org != scope.OrgID() {
		return errors.New("cross-tenant write")
	}
	f.certs[c.Serial] = c
	return nil
}

func (f *fakeStore) Revoke(_ context.Context, scope tenant.Scope, serial string, at time.Time) (bool, error) {
	c, ok := f.certs[serial]
	if !ok || c.Org != scope.OrgID() {
		return false, nil
	}
	if _, already := f.revoked[serial]; already {
		return false, nil
	}
	f.revoked[serial] = at
	return true, nil
}

func (f *fakeStore) Usable(_ context.Context, serial string, at time.Time) (bool, error) {
	c, ok := f.certs[serial]
	_, revoked := f.revoked[serial]
	return ok && !revoked && !at.Before(c.NotBefore) && at.Before(c.NotAfter), nil
}

func newBox(t *testing.T) *secrets.Box {
	t.Helper()
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.ParseKeys(line)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func clockAt(at *time.Time) func() time.Time { return func() time.Time { return *at } }

func scopeA(t *testing.T) tenant.Scope {
	t.Helper()
	s, err := tenant.ForAllGroups(orgA)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func csrDER(t *testing.T) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestBootstrap_CreatesAndStoresAnAuthorityOnce(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	first, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	second, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	if err != nil {
		t.Fatalf("second Bootstrap() error = %v", err)
	}
	if store.inserts != 1 || !first.TrustAnchor().Equal(second.TrustAnchor()) {
		t.Errorf("want one stored authority reused on restart, inserts=%d", store.inserts)
	}
}

func TestBootstrap_StoresPrivateKeysOnlySealed(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); err != nil {
		t.Fatal(err)
	}

	rec := store.authority
	for name, sealed := range map[string][]byte{"root": rec.RootKeySealed, "intermediate": rec.IntermediateKeySealed} {
		if _, err := x509.ParsePKCS8PrivateKey(sealed); err == nil {
			t.Errorf("%s key is stored in clear", name)
		}
	}
}

func TestBootstrap_FailsWithAnotherMasterKey(t *testing.T) {
	store, now := newFakeStore(), t0
	if _, err := agentca.Bootstrap(context.Background(), store, newBox(t), clockAt(&now)); err != nil {
		t.Fatal(err)
	}

	if _, err := agentca.Bootstrap(context.Background(), store, newBox(t), clockAt(&now)); !errors.Is(err, secrets.ErrDecrypt) {
		t.Errorf("Bootstrap() with a foreign master key error = %v, want ErrDecrypt", err)
	}
}

func TestBootstrap_SealedKeysCannotBeSwapped(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); err != nil {
		t.Fatal(err)
	}
	rec := store.authority
	rec.RootKeySealed, rec.IntermediateKeySealed = rec.IntermediateKeySealed, rec.RootKeySealed

	if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); err == nil {
		t.Error("swapped sealed keys must be rejected (associated data binds them to their certificate)")
	}
}

func TestBootstrap_ConcurrentCreationUsesTheWinner(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	winner, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	racing := &racingStore{fakeStore: store}

	loser, err := agentca.Bootstrap(context.Background(), racing, box, clockAt(&now))
	if err != nil {
		t.Fatalf("Bootstrap() after losing the race error = %v", err)
	}
	if !loser.TrustAnchor().Equal(winner.TrustAnchor()) {
		t.Error("the instance that lost the race must load the winner's authority")
	}
}

// racingStore pretends no authority exists on the first read (another instance inserts meanwhile).
type racingStore struct {
	*fakeStore
	reads int
}

func (r *racingStore) ActiveAuthority(ctx context.Context) (agentca.AuthorityRecord, bool, error) {
	r.reads++
	if r.reads == 1 {
		return agentca.AuthorityRecord{}, false, nil
	}
	return r.fakeStore.ActiveAuthority(ctx)
}

func TestIssue_RecordsTheCertificateAndItIsUsableUntilExpiry(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	issued, err := svc.IssueAgentCertificate(context.Background(), scopeA(t), csrDER(t), node)
	if err != nil {
		t.Fatalf("IssueAgentCertificate() error = %v", err)
	}
	rec, ok := store.certs[issued.Serial]
	if !ok || rec.Org != orgA || rec.Node != node {
		t.Fatalf("certificate not recorded for org/node: %+v", rec)
	}
	cert, _ := x509.ParseCertificate(issued.DER)
	if gotOrg, gotNode, err := pki.AgentIdentity(cert); err != nil || gotOrg != orgA || gotNode != node {
		t.Errorf("identity = %v, %v, %v", gotOrg, gotNode, err)
	}

	if usable, _ := svc.CertificateUsable(context.Background(), issued.Serial); !usable {
		t.Error("fresh certificate must be usable")
	}
	now = t0.Add(pki.AgentCertificateLifetime)
	if usable, _ := svc.CertificateUsable(context.Background(), issued.Serial); usable {
		t.Error("expired certificate must not be usable")
	}
}

func TestIssue_RequiresAValidScope(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if _, err := svc.IssueAgentCertificate(context.Background(), tenant.Scope{}, csrDER(t), node); err == nil {
		t.Error("issuing without tenant scope must fail")
	}
}

func TestRevoke_MakesTheCertificateUnusableImmediately(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	issued, _ := svc.IssueAgentCertificate(context.Background(), scopeA(t), csrDER(t), node)

	if err := svc.Revoke(context.Background(), scopeA(t), issued.Serial); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if usable, _ := svc.CertificateUsable(context.Background(), issued.Serial); usable {
		t.Error("revoked certificate must not be usable")
	}
	if err := svc.Revoke(context.Background(), scopeA(t), issued.Serial); !errors.Is(err, agentca.ErrNotFound) {
		t.Errorf("second Revoke() error = %v, want ErrNotFound", err)
	}
}

func TestCertificateUsable_UnknownSerialIsUnusable(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if usable, err := svc.CertificateUsable(context.Background(), "00000000000000000000000000000000"); err != nil || usable {
		t.Errorf("unknown serial: usable=%v err=%v, want false", usable, err)
	}
}

func TestTrustAnchor_IsTheRootCertificate(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if !svc.TrustAnchor().IsCA || !bytes.Equal(svc.TrustAnchor().RawSubject, svc.TrustAnchor().RawIssuer) {
		t.Error("trust anchor must be the self-signed root")
	}
}

// failingStore returns errs for the named operations.
type failingStore struct {
	*fakeStore
	fail map[string]bool
}

var errStore = errors.New("database unavailable")

func (f failingStore) ActiveAuthority(ctx context.Context) (agentca.AuthorityRecord, bool, error) {
	if f.fail["active"] {
		return agentca.AuthorityRecord{}, false, errStore
	}
	return f.fakeStore.ActiveAuthority(ctx)
}

func (f failingStore) InsertAuthority(ctx context.Context, r agentca.AuthorityRecord) error {
	if f.fail["insert"] {
		return errStore
	}
	return f.fakeStore.InsertAuthority(ctx, r)
}

func (f failingStore) RecordCertificate(ctx context.Context, s tenant.Scope, c agentca.CertificateRecord) error {
	if f.fail["record"] {
		return errStore
	}
	return f.fakeStore.RecordCertificate(ctx, s, c)
}

func (f failingStore) Revoke(ctx context.Context, s tenant.Scope, serial string, at time.Time) (bool, error) {
	if f.fail["revoke"] {
		return false, errStore
	}
	return f.fakeStore.Revoke(ctx, s, serial, at)
}

func (f failingStore) Usable(ctx context.Context, serial string, at time.Time) (bool, error) {
	if f.fail["usable"] {
		return false, errStore
	}
	return f.fakeStore.Usable(ctx, serial, at)
}

func TestService_PropagatesStoreErrors(t *testing.T) {
	box, now := newBox(t), t0
	for _, op := range []string{"active", "insert"} {
		store := failingStore{fakeStore: newFakeStore(), fail: map[string]bool{op: true}}
		if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); !errors.Is(err, errStore) {
			t.Errorf("Bootstrap with failing %s: error = %v, want errStore", op, err)
		}
	}

	store := failingStore{fakeStore: newFakeStore(), fail: map[string]bool{}}
	svc, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	if err != nil {
		t.Fatal(err)
	}
	store.fail["record"], store.fail["revoke"], store.fail["usable"] = true, true, true
	if _, err := svc.IssueAgentCertificate(context.Background(), scopeA(t), csrDER(t), node); !errors.Is(err, errStore) {
		t.Errorf("Issue: error = %v, want errStore", err)
	}
	if err := svc.Revoke(context.Background(), scopeA(t), "x"); !errors.Is(err, errStore) {
		t.Errorf("Revoke: error = %v, want errStore", err)
	}
	if _, err := svc.CertificateUsable(context.Background(), "x"); !errors.Is(err, errStore) {
		t.Errorf("Usable: error = %v, want errStore", err)
	}
}

func TestIssue_RejectsInvalidCSRs(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if _, err := svc.IssueAgentCertificate(context.Background(), scopeA(t), []byte("garbage"), node); !errors.Is(err, pki.ErrInvalidCSR) {
		t.Errorf("error = %v, want ErrInvalidCSR", err)
	}
	if len(store.certs) != 0 {
		t.Error("nothing must be recorded for a rejected CSR")
	}
}
