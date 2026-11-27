//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA = uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a")
	orgB = uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b")
)

type fixture struct {
	db     pgtest.Database
	appDSN string
}

var (
	setupOnce sync.Once
	fx        fixture
)

// setup migrates the shared test database once and seeds two organizations as owner.
func setup(t *testing.T) fixture {
	t.Helper()
	setupOnce.Do(func() {
		db := pgtest.Shared(t)
		if err := postgres.Migrate(context.Background(), db.OwnerDSN); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
		fx = fixture{db: db, appDSN: pgtest.EnableAppRole(t, db)}
		ownerExec(t, db.OwnerDSN,
			`INSERT INTO organizations (id, name) VALUES ($1, 'Org A'), ($2, 'Org B')`, orgA, orgB)
		ownerExec(t, db.OwnerDSN,
			`INSERT INTO users (id, email, display_name) VALUES
			   ('0193f5a0-0000-7000-8000-0000000000a1', 'alice@example.org', 'Alice'),
			   ('0193f5a0-0000-7000-8000-0000000000b1', 'bob@example.org', 'Bob')`)
		ownerExec(t, db.OwnerDSN,
			`INSERT INTO memberships (org_id, user_id) VALUES
			   ($1, '0193f5a0-0000-7000-8000-0000000000a1'), ($2, '0193f5a0-0000-7000-8000-0000000000b1')`, orgA, orgB)
	})
	return fx
}

func ownerExec(t *testing.T, dsn, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func open(t *testing.T, dsn string) *postgres.DB {
	t.Helper()
	db, err := postgres.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func scopeFor(t *testing.T, org uuid.UUID) tenant.Scope {
	t.Helper()
	scope, err := tenant.ForAllGroups(tenant.OrgID(org))
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestMigrate_IsIdempotent(t *testing.T) {
	f := setup(t)

	if err := postgres.Migrate(context.Background(), f.db.OwnerDSN); err != nil {
		t.Errorf("second Migrate() error = %v, want nil", err)
	}
}

func TestOpen_RefusesRolesThatBypassRowLevelSecurity(t *testing.T) {
	f := setup(t)

	_, err := postgres.Open(context.Background(), f.db.OwnerDSN) // superuser and owner

	if !errors.Is(err, postgres.ErrPrivilegedRole) {
		t.Errorf("Open(owner) error = %v, want ErrPrivilegedRole", err)
	}
}

// withoutAppAccess are tables with org_id in the columnstore, which has no row-level security:
// sermon_app has no right on them, the SECURITY DEFINER functions act in the current organization
// (V1-24, ADR 0044; like the metrics, ADR 0001).
var withoutAppAccess = map[string]bool{"log_counts_1m": true, "log_counts_1h": true}

func TestRLS_WithoutTenantNothingIsVisible(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	tables := []string{"organizations", "users"}
	rows, err := conn.Query(ctx, `SELECT DISTINCT table_name FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name = 'org_id' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "sessions" && !withoutAppAccess[name] { // sessions: instance-level, no RLS (ADR 0007)
			tables = append(tables, name)
		}
	}
	rows.Close()
	for name := range withoutAppAccess {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&n); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: sermon_app reads it (%d rows, %v), want permission denied", name, n, err)
		}
	}
	if len(tables) < 6 {
		t.Fatalf("found only %v – tenant tables not visible to the test", tables)
	}

	for _, table := range tables {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s: %d rows visible without tenant, want 0", table, n)
		}
	}
}

// TestRLS_EveryTenantTableIsForced fails when a new table with org_id lacks ENABLE + FORCE ROW
// LEVEL SECURITY (CLAUDE.md, ADR 0007).
func TestRLS_EveryTenantTableIsForced(t *testing.T) {
	// Instance-level tables that carry org_id without RLS, with their reason (ADR 0007).
	exempt := map[string]string{
		"sessions": "looked up by token hash before any tenant is known; only the session's own row is read",
	}
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
		  AND (c.relname IN ('organizations', 'users') OR EXISTS (
		      SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'org_id' AND NOT a.attisdropped))`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var enabled, forced bool
		if err := rows.Scan(&name, &enabled, &forced); err != nil {
			t.Fatal(err)
		}
		n++
		if _, ok := exempt[name]; ok || withoutAppAccess[name] {
			continue
		}
		if !enabled || !forced {
			t.Errorf("%s: row level security enabled=%v forced=%v, want both", name, enabled, forced)
		}
	}
	if n < 6 {
		t.Errorf("checked only %d tables", n)
	}
}

func TestInTenant_SeesOnlyItsOwnOrganizationAndUsers(t *testing.T) {
	db := open(t, setup(t).appDSN)

	var orgs []sqlcdb.ListOrganizationsRow
	var users []sqlcdb.ListUsersRow
	err := db.InTenant(context.Background(), scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		var err error
		if orgs, err = q.ListOrganizations(context.Background()); err != nil {
			return err
		}
		users, err = q.ListUsers(context.Background())
		return err
	})
	if err != nil {
		t.Fatalf("InTenant() error = %v", err)
	}

	if len(orgs) != 1 || orgs[0].ID != orgA {
		t.Errorf("organizations = %+v, want only Org A", orgs)
	}
	if len(users) != 1 || users[0].Email != "alice@example.org" {
		t.Errorf("users = %+v, want only alice", users)
	}
}

func TestInTenant_CannotReadOrWriteAnotherTenant(t *testing.T) {
	db := open(t, setup(t).appDSN)
	ctx := context.Background()

	err := db.InTenant(ctx, scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		_, err := q.GetOrganization(ctx, orgB)
		return err
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("reading Org B from Org A scope: error = %v, want ErrNoRows", err)
	}

	err = db.InTenant(ctx, scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		_, err := q.CreateOrganization(ctx, sqlcdb.CreateOrganizationParams{ID: uuid.New(), Name: "Intruder"})
		return err
	})
	if err == nil {
		t.Error("creating an organization outside the scope must be rejected by RLS")
	}
}

func TestInTenant_DoesNotLeakTheTenantToLaterQueries(t *testing.T) {
	db := open(t, setup(t).appDSN+"&pool_max_conns=1") // force reuse of the same connection
	ctx := context.Background()

	if err := db.InTenant(ctx, scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		_, err := q.ListOrganizations(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	n, err := db.CountVisibleOrganizations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("after the tenant transaction %d organizations are visible, want 0", n)
	}
}

func TestInTenant_RejectsAnInvalidScope(t *testing.T) {
	db := open(t, setup(t).appDSN)
	called := false

	err := db.InTenant(context.Background(), tenant.Scope{}, func(*sqlcdb.Queries) error {
		called = true
		return nil
	})

	if !errors.Is(err, postgres.ErrInvalidScope) || called {
		t.Errorf("InTenant(zero scope) error = %v, called = %v; want ErrInvalidScope, not called", err, called)
	}
}

func TestInTenant_RollsBackOnError(t *testing.T) {
	db := open(t, setup(t).appDSN)
	ctx := context.Background()
	id := uuid.New()
	boom := errors.New("boom")

	err := db.InTenant(ctx, scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		if err := q.CreateUser(ctx, sqlcdb.CreateUserParams{ID: id, Email: "rollback@example.org", DisplayName: "R"}); err != nil {
			return err
		}
		if err := q.AddMembership(ctx, sqlcdb.AddMembershipParams{OrgID: orgA, UserID: id}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTenant() error = %v, want boom", err)
	}

	var n int
	ctxConn, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := pgx.Connect(ctxConn, setup(t).db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM users WHERE id = $1", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("user from the failed transaction was persisted")
	}
}

func TestAppRole_CannotSwitchOffRowLevelSecurity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, "ALTER TABLE organizations DISABLE ROW LEVEL SECURITY"); err == nil {
		t.Error("app role must not be able to disable RLS")
	}
	if _, err := conn.Exec(ctx, "SET row_security = off"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM organizations").Scan(&n); err == nil {
		t.Errorf("with row_security=off the query must fail for an unprivileged role, got %d rows", n)
	}
}

// The development environment and operators create sermon_app (with LOGIN) before the first
// migration; the migration must then only grant privileges.
func TestMigrate_WorksWithAPreexistingAppRole(t *testing.T) {
	f := setup(t) // sermon_app exists cluster-wide after the first migration
	ownerExec(t, f.db.OwnerDSN, "CREATE DATABASE sermon_preexisting_role")

	ownerDSN := strings.Replace(f.db.OwnerDSN, "/sermon?", "/sermon_preexisting_role?", 1)
	appDSN := strings.Replace(f.appDSN, "/sermon?", "/sermon_preexisting_role?", 1)
	if err := postgres.Migrate(context.Background(), ownerDSN); err != nil {
		t.Fatalf("Migrate() with existing role error = %v", err)
	}

	db := open(t, appDSN)
	err := db.InTenant(context.Background(), scopeFor(t, orgA), func(q *sqlcdb.Queries) error {
		_, err := q.CreateOrganization(context.Background(), sqlcdb.CreateOrganizationParams{ID: orgA, Name: "Org A"})
		return err
	})
	if err != nil {
		t.Errorf("app role cannot use the migrated database: %v", err)
	}
}
