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

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type tokenOrg struct {
	mgmtOrg
	group tenant.GroupID
	other tenant.GroupID
}

func newTokenOrg(t *testing.T, f fixture) tokenOrg {
	t.Helper()
	o := newMgmtOrg(t, f)
	store := postgres.NewAccessStore(open(t, f.appDSN))
	to := tokenOrg{mgmtOrg: o, group: tenant.GroupID(uuid.New()), other: tenant.GroupID(uuid.New())}
	for i, g := range []tenant.GroupID{to.group, to.other} {
		if err := store.CreateGroup(context.Background(), o.scope, access.Group{ID: g, Name: string(rune('a' + i))}, entry(o.admin, "group.create")); err != nil {
			t.Fatal(err)
		}
	}
	return to
}

// issue stores a token valid for one hour.
func issue(t *testing.T, s *postgres.EnrollmentTokenStore, o tokenOrg, group tenant.GroupID, uses int) (enrollment.Token, [32]byte) {
	t.Helper()
	now := time.Now()
	tok := enrollment.Token{ID: enrollment.TokenID(uuid.New()), Group: group, CreatedBy: o.admin, CreatedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: uses}
	hash := sha256.Sum256([]byte(uuid.NewString()))
	scope, _ := tenant.ForGroups(o.scope.OrgID(), group)
	if err := s.Create(context.Background(), scope, tok, hash, entry(o.admin, "enrollment_token.create")); err != nil {
		t.Fatal(err)
	}
	return tok, hash
}

func TestEnrollmentTokenStore_LookupHonorsExpiryUsesAndRevocation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewEnrollmentTokenStore(open(t, f.appDSN))
	o := newTokenOrg(t, f)
	tok, hash := issue(t, s, o, o.group, 2)

	st, found, err := s.Lookup(ctx, hash, time.Now())
	if err != nil || !found || st.ID != tok.ID || st.Org != o.scope.OrgID() || st.Group != o.group || st.Hash != hash {
		t.Fatalf("Lookup() = %+v, %v, %v", st, found, err)
	}
	if _, found, _ := s.Lookup(ctx, hash, tok.ExpiresAt.Add(time.Second)); found {
		t.Error("expired tokens must not be found")
	}
	if err := s.Consume(ctx, st, time.Now(), entry(o.admin, "enrollment_token.use")); err != nil {
		t.Fatal(err)
	}
	if err := s.Consume(ctx, st, time.Now(), entry(o.admin, "enrollment_token.use")); err != nil {
		t.Fatal(err)
	}
	if err := s.Consume(ctx, st, time.Now(), entry(o.admin, "enrollment_token.use")); !errors.Is(err, enrollment.ErrInvalidToken) {
		t.Errorf("third use of a 2-use token: %v, want ErrInvalidToken", err)
	}
	if _, found, _ := s.Lookup(ctx, hash, time.Now()); found {
		t.Error("a used-up token must not be found")
	}

	other, otherHash := issue(t, s, o, o.group, 1)
	if err := s.Revoke(ctx, o.scope, other.ID, entry(o.admin, "enrollment_token.revoke")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Lookup(ctx, otherHash, time.Now()); found {
		t.Error("a revoked token must not be found")
	}
	if got := auditActions(t, postgres.NewAccessStore(open(t, f.appDSN)), o.scope); len(got) != 7 || got[6] != "enrollment_token.revoke" {
		t.Errorf("audit = %v", got)
	}
}

func TestEnrollmentTokenStore_ConcurrentRedemptionsNeverExceedMaxUses(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewEnrollmentTokenStore(open(t, f.appDSN))
	o := newTokenOrg(t, f)
	_, hash := issue(t, s, o, o.group, 3)
	st, _, _ := s.Lookup(ctx, hash, time.Now())

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Consume(ctx, st, time.Now(), entry(o.admin, "enrollment_token.use"))
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 3 {
		t.Errorf("%d redemptions succeeded, want exactly 3", ok)
	}
}

func TestEnrollmentTokenStore_ScopesByGroupAndOrganization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewEnrollmentTokenStore(open(t, f.appDSN))
	a, b := newTokenOrg(t, f), newTokenOrg(t, f)
	inGroup, _ := issue(t, s, a, a.group, 1)
	inOther, _ := issue(t, s, a, a.other, 1)

	onlyGroup, _ := tenant.ForGroups(a.scope.OrgID(), a.group)
	list, err := s.List(ctx, onlyGroup)
	if err != nil || len(list) != 1 || list[0].ID != inGroup.ID || list[0].CreatedBy != a.admin || list[0].MaxUses != 1 {
		t.Errorf("List(group) = %+v, %v; want only the group's token", list, err)
	}
	if err := s.Revoke(ctx, onlyGroup, inOther.ID, entry(a.admin, "x")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("revoking a token of another group: %v, want ErrNotFound", err)
	}
	if foreign, _ := s.List(ctx, b.scope); len(foreign) != 0 {
		t.Errorf("Org B sees %d tokens of Org A", len(foreign))
	}
	if err := s.Revoke(ctx, b.scope, inGroup.ID, entry(b.admin, "x")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("foreign revoke: %v, want ErrNotFound", err)
	}
	// A token cannot be bound to a group of another organization.
	foreignGroup, _ := tenant.ForGroups(b.scope.OrgID(), a.group)
	now := time.Now()
	err = s.Create(ctx, foreignGroup, enrollment.Token{ID: enrollment.TokenID(uuid.New()), Group: a.group, CreatedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: 1},
		sha256.Sum256([]byte(uuid.NewString())), entry(b.admin, "x"))
	if !errors.Is(err, access.ErrNotFound) {
		t.Errorf("token for a foreign group: %v, want ErrNotFound", err)
	}
}
