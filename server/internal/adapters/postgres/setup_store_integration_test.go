//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	firstrun "github.com/pmoscode/SerMon/server/internal/app/setup"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// resetSetup returns the instance to "not initialized" (the setup row is a singleton).
func resetSetup(t *testing.T, f fixture) {
	t.Helper()
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM superadmins")
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM instance_setup")
}

func firstAdmin() firstrun.FirstAdmin {
	id := uuid.New()
	return firstrun.FirstAdmin{
		User: identity.UserID(id), Email: id.String() + "@example.org", DisplayName: "Ann",
		PasswordHash: "$argon2id$v=19$fixture", Org: tenant.OrgID(uuid.New()), OrganizationName: "First", Binding: uuid.New(),
	}
}

func TestSetupStore_CompletesOnceAndCreatesTheFirstAdmin(t *testing.T) {
	f := setup(t)
	resetSetup(t, f)
	ctx := context.Background()
	db := open(t, f.appDSN)
	s := postgres.NewSetupStore(db)
	hash := sha256.Sum256([]byte("token-1"))
	now := time.Now()

	if done, err := s.Completed(ctx); err != nil || done {
		t.Fatalf("Completed() before setup = %v, %v", done, err)
	}
	if err := s.IssueToken(ctx, hash, now.Add(firstrun.TokenLifetime)); err != nil {
		t.Fatal(err)
	}
	admin := firstAdmin()
	if err := s.Complete(ctx, hash, now, admin); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	if done, _ := s.Completed(ctx); !done {
		t.Error("Completed() after setup = false")
	}
	// Second run: the token is used up and no new token can be issued.
	if err := s.Complete(ctx, hash, now, firstAdmin()); !errors.Is(err, firstrun.ErrInvalidToken) {
		t.Errorf("second Complete() = %v, want ErrInvalidToken", err)
	}
	if err := s.IssueToken(ctx, sha256.Sum256([]byte("token-2")), now.Add(time.Minute)); !errors.Is(err, firstrun.ErrAlreadyInitialized) {
		t.Errorf("IssueToken() after setup = %v, want ErrAlreadyInitialized", err)
	}

	// The admin is superadmin, org-wide admin of the new organization and can log in.
	accessStore := postgres.NewAccessStore(db)
	if super, err := accessStore.IsSuperadmin(ctx, admin.User); err != nil || !super {
		t.Errorf("IsSuperadmin() = %v, %v", super, err)
	}
	p, err := access.NewLoader(accessStore).Load(ctx, session.Session{User: admin.User, Org: admin.Org})
	if err != nil || !p.Superadmin || len(p.Bindings) != 1 || !p.Bindings[0].AllGroups || p.Bindings[0].Role.String() != "admin" {
		t.Errorf("principal = %+v, %v", p, err)
	}
	credentials := postgres.NewCredentialStore(db)
	user, found, err := credentials.FindUserByEmail(ctx, admin.Email)
	if err != nil || !found || user != admin.User {
		t.Errorf("FindUserByEmail() = %v, %v, %v", user, found, err)
	}
	if c, found, err := credentials.Get(ctx, admin.User); err != nil || !found || c.PasswordHash != admin.PasswordHash {
		t.Errorf("credentials = %+v, %v, %v", c, found, err)
	}
	records, _ := accessStore.ListAudit(ctx, scopeFor(t, uuid.UUID(admin.Org)), 0, 10)
	if len(records) != 1 || records[0].Action != "instance.setup" || records[0].Actor == nil || *records[0].Actor != admin.User {
		t.Errorf("audit = %+v", records)
	}
}

func TestSetupStore_TokensExpireAndAreReplaced(t *testing.T) {
	f := setup(t)
	resetSetup(t, f)
	ctx := context.Background()
	s := postgres.NewSetupStore(open(t, f.appDSN))
	old, current := sha256.Sum256([]byte("old")), sha256.Sum256([]byte("current"))
	t0 := time.Now()

	if err := s.IssueToken(ctx, old, t0.Add(firstrun.TokenLifetime)); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, old, t0.Add(firstrun.TokenLifetime+time.Second), firstAdmin()); !errors.Is(err, firstrun.ErrInvalidToken) {
		t.Errorf("expired token: %v, want ErrInvalidToken", err)
	}
	if err := s.IssueToken(ctx, current, t0.Add(firstrun.TokenLifetime)); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, old, t0, firstAdmin()); !errors.Is(err, firstrun.ErrInvalidToken) {
		t.Errorf("replaced token: %v, want ErrInvalidToken", err)
	}
	if err := s.Complete(ctx, sha256.Sum256([]byte("wrong")), t0, firstAdmin()); !errors.Is(err, firstrun.ErrInvalidToken) {
		t.Errorf("wrong token: %v, want ErrInvalidToken", err)
	}
	if done, _ := s.Completed(ctx); done {
		t.Error("failed attempts must not complete the setup")
	}
	if err := s.Complete(ctx, current, t0, firstAdmin()); err != nil {
		t.Errorf("current token: %v", err)
	}
}

func TestSetupStore_ConcurrentCompletionsHaveOneWinner(t *testing.T) {
	f := setup(t)
	resetSetup(t, f)
	ctx := context.Background()
	s := postgres.NewSetupStore(open(t, f.appDSN))
	hash := sha256.Sum256([]byte("race"))
	now := time.Now()
	if err := s.IssueToken(ctx, hash, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Complete(ctx, hash, now, firstAdmin())
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		}
	}
	var n int
	conn, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM superadmins").Scan(&n)
	if wins != 1 || n != 1 {
		t.Errorf("%d winners, %d superadmins; want 1 and 1 (errors %v)", wins, n, errs)
	}
}
