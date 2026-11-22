//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
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

// certificate returns a certificate record for a new node of org (created with the owner DSN;
// certificates reference their node since migration 00010).
func certificate(t *testing.T, ownerDSN string, org uuid.UUID) agentca.CertificateRecord {
	t.Helper()
	node := uuid.New()
	createNode(t, ownerDSN, org, node)
	return agentca.CertificateRecord{
		Serial: randomSerial(), Org: tenant.OrgID(org), Node: identity.NodeID(node), Authority: issuingAuthority(t, ownerDSN),
		NotBefore: caTime.Add(-time.Minute), NotAfter: caTime.Add(30 * 24 * time.Hour),
	}
}

// issuingAuthority returns the ID of the issuing CA, creating a placeholder row if there is none
// (certificates reference their CA since migration 00012).
func issuingAuthority(t *testing.T, ownerDSN string) int32 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var id int32
	err = conn.QueryRow(ctx, "SELECT id FROM pki_authorities WHERE active").Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conn.QueryRow(ctx, `INSERT INTO pki_authorities (root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed)
			VALUES ('\x01', '\x01', '\x01', '\x01') RETURNING id`).Scan(&id)
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// createNode inserts a node (and a group for it) into org as the schema owner.
func createNode(t *testing.T, ownerDSN string, org, node uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	group := uuid.New()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"SELECT set_config('sermon.org_id', $1, false)", []any{org.String()}},
		{"INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, $3)", []any{group, org, "g-" + group.String()[:8]}},
		{
			`INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes)
		  VALUES ($1, $2, $3, 'host', $4, 'debian', '13', '6.12.0', 'amd64', 2, 1073741824)`,
			[]any{node, org, group, strings.ReplaceAll(node.String(), "-", "")},
		},
	} {
		if _, err := conn.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
}

func TestAgentCAStore_AuthorityIsStoredOnceAndLoadedAgain(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM agent_certificates") // they reference their CA
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM pki_authorities")
	store := postgres.NewAgentCAStore(open(t, f.appDSN))
	record := agentca.AuthorityRecord{RootCert: []byte("root"), RootKeySealed: []byte("rk"), IntermediateCert: []byte("int"), IntermediateKeySealed: []byte("ik")}

	if err := store.InsertAuthority(ctx, record); err != nil {
		t.Fatalf("InsertAuthority() error = %v", err)
	}
	if err := store.InsertAuthority(ctx, record); !errors.Is(err, agentca.ErrAuthorityExists) {
		t.Errorf("second InsertAuthority() error = %v, want ErrAuthorityExists", err)
	}
	trusted, err := store.TrustedAuthorities(ctx)
	if err != nil || len(trusted) != 1 || !trusted[0].Issuing || string(trusted[0].Record.IntermediateKeySealed) != "ik" {
		t.Errorf("TrustedAuthorities() = %+v, %v", trusted, err)
	}
}

func TestAgentCAStore_RolloverAndRetirement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM agent_certificates")
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM pki_authorities")
	store := postgres.NewAgentCAStore(open(t, f.appDSN))
	rec := func(name string) agentca.AuthorityRecord {
		return agentca.AuthorityRecord{RootCert: []byte(name), RootKeySealed: []byte("k"), IntermediateCert: []byte("i"), IntermediateKeySealed: []byte("k")}
	}
	if err := store.InsertAuthority(ctx, rec("old")); err != nil {
		t.Fatal(err)
	}
	cert := certificate(t, f.db.OwnerDSN, orgA) // issued by the old CA
	if err := store.RecordCertificate(ctx, scopeFor(t, orgA), cert); err != nil {
		t.Fatal(err)
	}
	super := identity.UserID(uuid.New())

	// Two instances start a rollover at the same time: exactly one succeeds.
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- store.StartRollover(ctx, rec("new"), super) }()
	}
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) || (!errors.Is(first, agentca.ErrRolloverInProgress) && !errors.Is(second, agentca.ErrRolloverInProgress)) {
		t.Fatalf("concurrent rollovers: %v / %v; want exactly one", first, second)
	}
	trusted, _ := store.TrustedAuthorities(ctx)
	if len(trusted) != 2 || trusted[0].Issuing || !trusted[1].Issuing || string(trusted[1].Record.RootCert) != "new" {
		t.Fatalf("trusted = %+v", trusted)
	}
	oldID, newID := trusted[0].ID, trusted[1].ID

	if err := store.Retire(ctx, newID, super, caTime); !errors.Is(err, agentca.ErrIssuingAuthority) {
		t.Errorf("retire issuing: %v", err)
	}
	if err := store.Retire(ctx, oldID, super, caTime); !errors.Is(err, agentca.ErrAuthorityInUse) {
		t.Errorf("retire with a valid certificate: %v, want ErrAuthorityInUse", err)
	}
	states, _ := store.Authorities(ctx, caTime)
	if len(states) != 2 || states[0].CertificatesInUse != 1 || states[0].RootFingerprint == "" {
		t.Errorf("states = %+v", states)
	}
	// After expiry (or revocation) of the old certificates the old CA can be retired.
	if err := store.Retire(ctx, oldID, super, cert.NotAfter.Add(time.Second)); err != nil {
		t.Fatalf("Retire() after expiry: %v", err)
	}
	if err := store.Retire(ctx, oldID, super, cert.NotAfter.Add(time.Second)); !errors.Is(err, agentca.ErrUnknownAuthority) {
		t.Errorf("second retire: %v, want ErrUnknownAuthority", err)
	}
	if trusted, _ := store.TrustedAuthorities(ctx); len(trusted) != 1 || trusted[0].ID != newID {
		t.Errorf("after retirement trusted = %+v", trusted)
	}
	if err := store.StartRollover(ctx, rec("newer"), super); err != nil {
		t.Errorf("a new rollover is possible after the retirement: %v", err)
	}

	// Every CA action left exactly one entry in the instance audit log, the failed ones none.
	audit := postgres.NewInstanceAuditStore(open(t, f.appDSN))
	entries, err := audit.List(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	byTarget := map[string][]string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Action, "pki.") {
			who := "system"
			if e.Actor != nil && *e.Actor == super {
				who = "super"
			}
			byTarget[e.Target] = append(byTarget[e.Target], e.Action+" by "+who)
		}
	}
	oldTarget, newTarget := fmt.Sprintf("ca:%d", oldID), fmt.Sprintf("ca:%d", newID)
	if got := byTarget[oldTarget]; len(got) != 2 || got[0] != "pki.authority_retired by super" || got[1] != "pki.authority_created by system" {
		t.Errorf("entries of the old CA = %v", got)
	}
	if got := byTarget[newTarget]; len(got) != 1 || got[0] != "pki.rollover_started by super" {
		t.Errorf("entries of the new CA = %v (the losing concurrent rollover must not log)", got)
	}
	if err := audit.Verify(ctx); err != nil {
		t.Errorf("Verify() = %v", err)
	}
}

func TestInstanceAuditLog_DetectsTamperingAndIsAppendOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ca := postgres.NewAgentCAStore(open(t, f.appDSN))
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM agent_certificates")
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM pki_authorities")
	if err := ca.InsertAuthority(ctx, agentca.AuthorityRecord{RootCert: []byte("r"), RootKeySealed: []byte("k"), IntermediateCert: []byte("i"), IntermediateKeySealed: []byte("k")}); err != nil {
		t.Fatal(err)
	}
	audit := postgres.NewInstanceAuditStore(open(t, f.appDSN))
	if err := audit.Verify(ctx); err != nil {
		t.Fatalf("Verify() = %v", err)
	}

	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, sql := range []string{"UPDATE instance_audit_log SET action = 'x'", "DELETE FROM instance_audit_log"} {
		if _, err := conn.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s as sermon_app: %v, want permission denied", sql, err)
		}
	}

	ownerExec(t, f.db.OwnerDSN, `UPDATE instance_audit_log SET details = '{"root_fingerprint": "forged"}' WHERE id = (SELECT max(id) FROM instance_audit_log)`)
	if err := audit.Verify(ctx); !errors.Is(err, postgres.ErrAuditChainBroken) {
		t.Errorf("after tampering: Verify() = %v, want ErrAuditChainBroken", err)
	}
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM instance_audit_log") // leave a valid chain for other tests
}

func TestAgentCAStore_CertificatesAreTenantIsolatedButCheckableWithoutTenant(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAgentCAStore(open(t, f.appDSN))
	certA := certificate(t, f.db.OwnerDSN, orgA)

	if err := store.RecordCertificate(ctx, scopeFor(t, orgA), certA); err != nil {
		t.Fatalf("RecordCertificate() error = %v", err)
	}
	if usable, err := store.Usable(ctx, certA.Serial, certA.Org, certA.Node, caTime); err != nil || !usable {
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
	if err := store.RecordCertificate(ctx, scopeFor(t, orgB), certificate(t, f.db.OwnerDSN, orgA)); err == nil {
		t.Error("recording a certificate for another organization must fail")
	}
}

func TestAgentCAStore_UsableHonorsRevocationAndValidity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAgentCAStore(open(t, f.appDSN))
	cert := certificate(t, f.db.OwnerDSN, orgA)
	if err := store.RecordCertificate(ctx, scopeFor(t, orgA), cert); err != nil {
		t.Fatal(err)
	}

	cases := map[string]time.Time{
		"before not_before": cert.NotBefore.Add(-time.Second),
		"at not_after":      cert.NotAfter,
	}
	for name, at := range cases {
		if usable, _ := store.Usable(ctx, cert.Serial, cert.Org, cert.Node, at); usable {
			t.Errorf("%s: usable, want unusable", name)
		}
	}
	if usable, _ := store.Usable(ctx, randomSerial(), cert.Org, cert.Node, caTime); usable {
		t.Error("unknown serial must be unusable (fail closed)")
	}
	// The serial is bound to the identity in the certificate (M1-38): a forged certificate (only
	// with a compromised intermediate) can't reuse a known valid serial for another org or node.
	if usable, _ := store.Usable(ctx, cert.Serial, cert.Org, cert.Node, caTime); !usable {
		t.Fatal("recorded certificate must be usable")
	}
	if usable, _ := store.Usable(ctx, cert.Serial, tenant.OrgID(uuid.New()), cert.Node, caTime); usable {
		t.Error("the serial with another organization must be unusable")
	}
	if usable, _ := store.Usable(ctx, cert.Serial, cert.Org, identity.NodeID(uuid.New()), caTime); usable {
		t.Error("the serial with another node must be unusable")
	}

	if revoked, err := store.Revoke(ctx, scopeFor(t, orgA), cert.Serial, caTime); err != nil || !revoked {
		t.Fatalf("Revoke() = %v, %v", revoked, err)
	}
	if usable, _ := store.Usable(ctx, cert.Serial, cert.Org, cert.Node, caTime); usable {
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
	if _, err := conn.Exec(ctx, "INSERT INTO organizations (id, name) VALUES ($1, 'Org A ' || $1::uuid::text)", orgA); err == nil {
		t.Error("FORCE RLS must stop the owner from writing without tenant")
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', $1, false)", orgA.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO organizations (id, name) VALUES ($1, 'Org A ' || $1::uuid::text)", orgA); err != nil {
		t.Fatalf("insert organization with tenant: %v", err)
	}

	store := postgres.NewAgentCAStore(open(t, appDSN))
	cert := certificate(t, ownerDSN, orgA)
	if err := store.RecordCertificate(ctx, scopeFor(t, orgA), cert); err != nil {
		t.Fatalf("RecordCertificate() error = %v", err)
	}
	if usable, err := store.Usable(ctx, cert.Serial, cert.Org, cert.Node, caTime); err != nil || !usable {
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
