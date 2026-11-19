//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	alice = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	bob   = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
)

const argonHash = "$argon2id$v=19$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"

func TestCredentialStore_FindsUsersByEmailWithoutTenant(t *testing.T) {
	store := postgres.NewCredentialStore(open(t, setup(t).appDSN))
	ctx := context.Background()

	for _, email := range []string{"alice@example.org", "ALICE@Example.org"} {
		id, found, err := store.FindUserByEmail(ctx, email)
		if err != nil || !found || id != alice {
			t.Errorf("FindUserByEmail(%q) = %v, %v, %v; want alice", email, id, found, err)
		}
	}
	if _, found, err := store.FindUserByEmail(ctx, "nobody@example.org"); err != nil || found {
		t.Errorf("unknown e-mail: found=%v err=%v, want false", found, err)
	}
}

func TestCredentialStore_OrganizationsOfAUserOnly(t *testing.T) {
	store := postgres.NewCredentialStore(open(t, setup(t).appDSN))

	orgs, err := store.Organizations(context.Background(), alice)
	if err != nil || len(orgs) != 1 || orgs[0].ID != tenant.OrgID(orgA) || orgs[0].Name != "Org A" {
		t.Errorf("Organizations(alice) = %+v, %v; want only Org A", orgs, err)
	}
}

func TestCredentialStore_PasswordLifecycle(t *testing.T) {
	store := postgres.NewCredentialStore(open(t, setup(t).appDSN))
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	if _, found, err := store.Get(ctx, bob); err != nil || found {
		t.Fatalf("bob has no credentials yet: found=%v err=%v", found, err)
	}
	if err := store.SetPassword(ctx, bob, argonHash, now); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	lock := now.Add(time.Minute)
	if err := store.RecordFailedLogin(ctx, bob, &lock); err != nil {
		t.Fatal(err)
	}
	c, found, err := store.Get(ctx, bob)
	if err != nil || !found || c.PasswordHash != argonHash || c.FailedAttempts != 1 || c.LockedUntil == nil || !c.LockedUntil.Equal(lock) {
		t.Fatalf("Get() = %+v, %v, %v", c, found, err)
	}
	if err := store.ResetFailedLogins(ctx, bob); err != nil {
		t.Fatal(err)
	}
	rehashed := strings.Replace(argonHash, "m=8192", "m=16384", 1)
	if err := store.UpdatePasswordHash(ctx, bob, rehashed); err != nil {
		t.Fatal(err)
	}
	c, _, _ = store.Get(ctx, bob)
	if c.FailedAttempts != 0 || c.LockedUntil != nil || c.PasswordHash != rehashed {
		t.Errorf("after reset/rehash: %+v", c)
	}
	if err := store.SetPassword(ctx, bob, "plaintext", now); err == nil {
		t.Error("non-argon2id values must be rejected by the database")
	}
}

func TestSessionStore_Lifecycle(t *testing.T) {
	store := postgres.NewSessionStore(open(t, setup(t).appDSN))
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	id := strings.Repeat("ab", 32)
	s := session.Session{ID: id, User: alice, Org: tenant.OrgID(orgA), CreatedAt: now, LastSeenAt: now, StepUpAt: now}

	if err := store.Insert(ctx, s); err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	later := now.Add(5 * time.Minute)
	if err := store.Touch(ctx, id, later); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStepUp(ctx, id, later); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get(ctx, id)
	if err != nil || !found || got.User != alice || got.Org != tenant.OrgID(orgA) || !got.LastSeenAt.Equal(later) || !got.StepUpAt.Equal(later) {
		t.Fatalf("Get() = %+v, %v, %v", got, found, err)
	}
	if err := store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, id); found {
		t.Error("deleted session must be gone")
	}
}

func TestSessionStore_InstanceSessionWithoutOrganization(t *testing.T) {
	store := postgres.NewSessionStore(open(t, setup(t).appDSN))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := strings.Repeat("cd", 32)

	if err := store.Insert(ctx, session.Session{ID: id, User: bob, CreatedAt: now, LastSeenAt: now, StepUpAt: now}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get(ctx, id)
	if got.Org != (tenant.OrgID{}) {
		t.Errorf("Org = %v, want zero", got.Org)
	}
	if err := store.DeleteForUser(ctx, bob); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, id); found {
		t.Error("DeleteForUser must remove the user's sessions")
	}
}

// With a non-superuser owner FORCE RLS applies to the owner; the checks still work because the
// SECURITY DEFINER functions run as sermon_definer.
func TestAuthLookups_WorkWithANonSuperuserOwner(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ownerDSN, appDSN := nonSuperuserDatabase(t, f, "sermon_nonsuper_auth")

	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, sql := range []string{
		"SELECT set_config('sermon.org_id', '" + orgA.String() + "', false)",
		"INSERT INTO organizations (id, name) VALUES ('" + orgA.String() + "', 'Org A')",
		"INSERT INTO users (id, email, display_name) VALUES ('" + uuid.UUID(alice).String() + "', 'alice@example.org', 'Alice')",
		"INSERT INTO memberships (org_id, user_id) VALUES ('" + orgA.String() + "', '" + uuid.UUID(alice).String() + "')",
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	store := postgres.NewCredentialStore(open(t, appDSN))
	if id, found, err := store.FindUserByEmail(ctx, "alice@example.org"); err != nil || !found || id != alice {
		t.Errorf("FindUserByEmail() = %v, %v, %v", id, found, err)
	}
	if orgs, err := store.Organizations(ctx, alice); err != nil || len(orgs) != 1 {
		t.Errorf("Organizations() = %+v, %v", orgs, err)
	}
}

func TestDefinerRole_HasNoCreatePrivilegeAndNoLogin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var canCreate, canLogin, bypass bool
	err = conn.QueryRow(ctx, `SELECT has_schema_privilege('sermon_definer', 'public', 'CREATE'), rolcanlogin, rolbypassrls
		FROM pg_roles WHERE rolname = 'sermon_definer'`).Scan(&canCreate, &canLogin, &bypass)
	if err != nil {
		t.Fatal(err)
	}
	if canCreate || canLogin || bypass {
		t.Errorf("sermon_definer: CREATE=%v LOGIN=%v BYPASSRLS=%v, want all false", canCreate, canLogin, bypass)
	}

	var memberOfDefiner bool
	if err := conn.QueryRow(ctx, "SELECT pg_has_role('sermon_app', 'sermon_definer', 'MEMBER')").Scan(&memberOfDefiner); err != nil {
		t.Fatal(err)
	}
	if memberOfDefiner {
		t.Error("sermon_app must not be a member of sermon_definer")
	}
}
