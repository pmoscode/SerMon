//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var caTime = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func randomSerial() string { return strings.ReplaceAll(uuid.NewString(), "-", "") }

func certificate(org uuid.UUID) agentca.CertificateRecord {
	return agentca.CertificateRecord{
		Serial: randomSerial(), Org: tenant.OrgID(org), Node: identity.NodeID(uuid.New()),
		NotBefore: caTime.Add(-time.Minute), NotAfter: caTime.Add(30 * 24 * time.Hour),
	}
}

func TestAgentCAStore_AuthorityIsStoredOnceAndLoadedAgain(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM pki_authorities")
	store := postgres.NewAgentCAStore(open(t, f.appDSN))
	record := agentca.AuthorityRecord{RootCert: []byte("root"), RootKeySealed: []byte("rk"), IntermediateCert: []byte("int"), IntermediateKeySealed: []byte("ik")}

	if err := store.InsertAuthority(ctx, record); err != nil {
		t.Fatalf("InsertAuthority() error = %v", err)
	}
	if err := store.InsertAuthority(ctx, record); !errors.Is(err, agentca.ErrAuthorityExists) {
		t.Errorf("second InsertAuthority() error = %v, want ErrAuthorityExists", err)
	}
	loaded, found, err := store.ActiveAuthority(ctx)
	if err != nil || !found || string(loaded.IntermediateKeySealed) != "ik" {
		t.Errorf("ActiveAuthority() = %+v, %v, %v", loaded, found, err)
	}
}

func TestAgentCAStore_CertificatesAreTenantIsolatedButCheckableWithoutTenant(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAgentCAStore(open(t, f.appDSN))
	certA := certificate(orgA)

	if err := store.RecordCertificate(ctx, scopeFor(t, orgA), certA); err != nil {
		t.Fatalf("RecordCertificate() error = %v", err)
	}
	if usable, err := store.Usable(ctx, certA.Serial, caTime); err != nil || !usable {
		t.Errorf("Usable() without tenant = %v, %v; want true", usable, err)
	}

	// Org B can neither revoke nor see Org A's certificate.
	if revoked, err := store.Revoke(ctx, scopeFor(t, orgB), certA.Serial, caTime); err != nil || revoked {
		t.Errorf("Revoke() from Org B = %v, %v; want false", revoked, err)
	}
	var visible int
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM agent_certificates").Scan(&visible); err != nil || visible != 0 {
		t.Errorf("agent_certificates visible without tenant: %d (err %v), want 0", visible, err)
	}

	// Writing a certificate for Org A from Org B's scope is rejected by RLS.
	if err := store.RecordCertificate(ctx, scopeFor(t, orgB), certificate(orgA)); err == nil {
		t.Error("recording a certificate for another organization must fail")
	}
}

func TestAgentCAStore_UsableHonorsRevocationAndValidity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAgentCAStore(open(t, f.appDSN))
	cert := certificate(orgA)
	if err := store.RecordCertificate(ctx, scopeFor(t, orgA), cert); err != nil {
		t.Fatal(err)
	}

	cases := map[string]time.Time{
		"before not_before": cert.NotBefore.Add(-time.Second),
		"at not_after":      cert.NotAfter,
	}
	for name, at := range cases {
		if usable, _ := store.Usable(ctx, cert.Serial, at); usable {
			t.Errorf("%s: usable, want unusable", name)
		}
	}
	if usable, _ := store.Usable(ctx, randomSerial(), caTime); usable {
		t.Error("unknown serial must be unusable (fail closed)")
	}

	if revoked, err := store.Revoke(ctx, scopeFor(t, orgA), cert.Serial, caTime); err != nil || !revoked {
		t.Fatalf("Revoke() = %v, %v", revoked, err)
	}
	if usable, _ := store.Usable(ctx, cert.Serial, caTime); usable {
		t.Error("revoked certificate must be unusable")
	}
	if revoked, _ := store.Revoke(ctx, scopeFor(t, orgA), cert.Serial, caTime); revoked {
		t.Error("revoking twice must report false")
	}
}

// In production the schema owner is not a superuser. The revocation check must still work:
// the SECURITY DEFINER function relies on owner access to agent_certificates (no FORCE RLS).
func TestAgentCAStore_RevocationCheckWorksWithANonSuperuserOwner(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ownerDSN, appDSN := nonSuperuserDatabase(t, f, "sermon_nonsuper")

	// FORCE RLS applies to a non-superuser owner as well: the fixture needs the tenant set.
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "INSERT INTO organizations (id, name) VALUES ($1, 'Org A')", orgA); err == nil {
		t.Error("FORCE RLS must stop the owner from writing without tenant")
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', $1, false)", orgA.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO organizations (id, name) VALUES ($1, 'Org A')", orgA); err != nil {
		t.Fatalf("insert organization with tenant: %v", err)
	}

	store := postgres.NewAgentCAStore(open(t, appDSN))
	cert := certificate(orgA)
	if err := store.RecordCertificate(ctx, scopeFor(t, orgA), cert); err != nil {
		t.Fatalf("RecordCertificate() error = %v", err)
	}
	if usable, err := store.Usable(ctx, cert.Serial, caTime); err != nil || !usable {
		t.Errorf("Usable() with non-superuser owner = %v, %v; want true", usable, err)
	}
}

// nonSuperuserDatabase creates a database owned by a non-superuser role (managed PostgreSQL setup),
// migrates it as that role and returns owner and app connection strings.
func nonSuperuserDatabase(t *testing.T, f fixture, name string) (ownerDSN, appDSN string) {
	t.Helper()
	ownerExec(t, f.db.OwnerDSN, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_owner_nonsuper') THEN
			CREATE ROLE sermon_owner_nonsuper LOGIN PASSWORD 'owner-test' NOSUPERUSER CREATEROLE;
		END IF; END $$`)
	// One-time provisioning by a superuser/DBA: sermon_definer exists cluster-wide (created by an
	// earlier migration), so the non-superuser owner cannot grant it to itself.
	ownerExec(t, f.db.OwnerDSN, "GRANT sermon_definer TO sermon_owner_nonsuper")
	ownerExec(t, f.db.OwnerDSN, "CREATE DATABASE "+name+" OWNER sermon_owner_nonsuper")
	ownerDSN = strings.Replace(strings.Replace(f.db.OwnerDSN, "/sermon?", "/"+name+"?", 1),
		"sermon:sermon-test@", "sermon_owner_nonsuper:owner-test@", 1)
	if err := postgres.Migrate(context.Background(), ownerDSN); err != nil {
		t.Fatalf("Migrate() as non-superuser owner error = %v", err)
	}
	return ownerDSN, strings.Replace(f.appDSN, "/sermon?", "/"+name+"?", 1)
}

// Without the one-time GRANT the migration must stop with an actionable message.
func TestMigrate_ExplainsTheMissingDefinerGrant(t *testing.T) {
	f := setup(t)
	ownerExec(t, f.db.OwnerDSN, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_owner_nogrant') THEN
			CREATE ROLE sermon_owner_nogrant LOGIN PASSWORD 'owner-test' NOSUPERUSER CREATEROLE;
		END IF; END $$`)
	ownerExec(t, f.db.OwnerDSN, "CREATE DATABASE sermon_nogrant OWNER sermon_owner_nogrant")
	dsn := strings.Replace(strings.Replace(f.db.OwnerDSN, "/sermon?", "/sermon_nogrant?", 1),
		"sermon:sermon-test@", "sermon_owner_nogrant:owner-test@", 1)

	err := postgres.Migrate(context.Background(), dsn)
	if err == nil || !strings.Contains(err.Error(), "not a member of sermon_definer") {
		t.Errorf("Migrate() error = %v, want a hint about GRANT sermon_definer", err)
	}
}
