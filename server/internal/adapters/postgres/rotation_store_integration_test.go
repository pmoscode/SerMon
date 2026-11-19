//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func enrolledNode(t *testing.T, f fixture) (rotation.Identity, *postgres.AgentCAStore) {
	t.Helper()
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Rotation')", org)
	cert := certificate(t, f.db.OwnerDSN, org)
	ca := postgres.NewAgentCAStore(open(t, f.appDSN))
	if err := ca.RecordCertificate(context.Background(), scopeFor(t, org), cert); err != nil {
		t.Fatal(err)
	}
	return rotation.Identity{Org: tenant.OrgID(org), Node: cert.Node, Serial: cert.Serial}, ca
}

func nextCertificate(id rotation.Identity) agentca.CertificateRecord {
	return agentca.CertificateRecord{
		Serial: randomSerial(), Org: id.Org, Node: id.Node,
		NotBefore: caTime.Add(-time.Minute), NotAfter: caTime.Add(30 * 24 * time.Hour),
	}
}

func TestRotationStore_OldCertificateWorksUntilTheEndOfTheGracePeriod(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id, ca := enrolledNode(t, f)
	s := postgres.NewRotationStore(open(t, f.appDSN))
	next := nextCertificate(id)

	if err := s.Rotate(ctx, id, next, caTime.Add(rotation.Grace), access.AuditEntry{Action: "certificate.rotate", Target: "n"}); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	for at, want := range map[time.Time]bool{caTime.Add(30 * time.Minute): true, caTime.Add(2 * time.Hour): false} {
		if usable, _ := ca.Usable(ctx, id.Serial, at); usable != want {
			t.Errorf("old certificate at %v usable = %v, want %v", at.Sub(caTime), usable, want)
		}
	}
	if usable, _ := ca.Usable(ctx, next.Serial, caTime.Add(2*time.Hour)); !usable {
		t.Error("the new certificate must be usable")
	}
	// The old certificate cannot rotate again.
	if err := s.Rotate(ctx, id, nextCertificate(id), caTime.Add(rotation.Grace), access.AuditEntry{Action: "x", Target: "n"}); !errors.Is(err, rotation.ErrSuperseded) {
		t.Errorf("second rotation with the old certificate: %v, want ErrSuperseded", err)
	}
}

func TestRotationStore_LockRevokesEveryCertificateOfTheNode(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id, ca := enrolledNode(t, f)
	s := postgres.NewRotationStore(open(t, f.appDSN))
	next := nextCertificate(id)
	if err := s.Rotate(ctx, id, next, caTime.Add(rotation.Grace), access.AuditEntry{Action: "certificate.rotate", Target: "n"}); err != nil {
		t.Fatal(err)
	}

	if err := s.Lock(ctx, id.Org, id.Node, "second session", caTime, access.AuditEntry{Action: "security.node_locked", Target: "n"}); err != nil {
		t.Fatalf("Lock() error = %v", err)
	}
	for _, serial := range []string{id.Serial, next.Serial} {
		if usable, _ := ca.Usable(ctx, serial, caTime.Add(time.Minute)); usable {
			t.Errorf("certificate %s still usable after the lock", serial)
		}
	}
	// A certificate issued later (e.g. by mistake) is unusable as long as the node is locked.
	later := nextCertificate(id)
	if err := ca.RecordCertificate(ctx, scopeFor(t, uuid.UUID(id.Org)), later); err != nil {
		t.Fatal(err)
	}
	if usable, _ := ca.Usable(ctx, later.Serial, caTime.Add(time.Minute)); usable {
		t.Error("certificates of a locked node must be unusable")
	}
}

func TestRotationStore_RecordSessionReturnsThePreviousIP(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id, _ := enrolledNode(t, f)
	s := postgres.NewRotationStore(open(t, f.appDSN))
	for i, tc := range []struct{ ip, previous string }{{"192.0.2.1", ""}, {"192.0.2.2", "192.0.2.1"}} {
		prev, err := s.RecordSession(ctx, id.Org, id.Node, tc.ip)
		if err != nil || prev != tc.previous {
			t.Errorf("session %d: previous = %q, %v; want %q", i, prev, err, tc.previous)
		}
	}
	other := identity.NodeID(uuid.New())
	if _, err := s.RecordSession(ctx, id.Org, other, "192.0.2.3"); err == nil {
		t.Error("unknown node must fail")
	}
}
