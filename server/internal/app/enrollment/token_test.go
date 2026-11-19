package enrollment_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	ann    = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	group1 = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
	group2 = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c2"))
)

type fakeStore struct {
	created  enrollment.Token
	hash     [32]byte
	scope    tenant.Scope
	audits   []access.AuditEntry
	lookup   *enrollment.Stored
	consumed enrollment.TokenID
	consume  error
	calls    int
}

func (f *fakeStore) Create(_ context.Context, scope tenant.Scope, t enrollment.Token, hash [32]byte, a access.AuditEntry) error {
	f.calls++
	f.scope, f.created, f.hash = scope, t, hash
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) List(_ context.Context, scope tenant.Scope) ([]enrollment.Token, error) {
	f.calls++
	f.scope = scope
	return nil, nil
}

func (f *fakeStore) Revoke(_ context.Context, scope tenant.Scope, _ enrollment.TokenID, a access.AuditEntry) error {
	f.calls++
	f.scope = scope
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) Lookup(_ context.Context, hash [32]byte, _ time.Time) (enrollment.Stored, bool, error) {
	f.hash = hash
	if f.lookup == nil {
		return enrollment.Stored{}, false, nil
	}
	return *f.lookup, true, nil
}

func (f *fakeStore) Consume(_ context.Context, s enrollment.Stored, _ time.Time, a access.AuditEntry) error {
	f.consumed = s.ID
	f.audits = append(f.audits, a)
	return f.consume
}

func service(store *fakeStore) *enrollment.Service {
	return enrollment.NewService(store, authz.NewPolicy(), func() time.Time { return now }, uuid.New)
}

func caller(r role.Role, groups ...tenant.GroupID) access.Caller {
	var bindings []authz.Binding
	for _, g := range groups {
		bindings = append(bindings, authz.Binding{Group: g, Role: r})
	}
	return access.Caller{Principal: authz.Principal{User: ann, Org: orgA, Bindings: bindings}, StepUpAt: now}
}

func TestCreate_IssuesAPrefixedTokenAndStoresOnlyItsHash(t *testing.T) {
	store := &fakeStore{}
	secret, tok, err := service(store).Create(context.Background(), caller(role.Operator, group1), group1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "sme_") || len(secret) != 47 {
		t.Errorf("token %q, want sme_ + 43 characters", secret)
	}
	if store.hash != sha256.Sum256([]byte(secret)) {
		t.Error("the store must get the SHA-256 of the whole token")
	}
	if tok.Group != group1 || tok.MaxUses != 1 || !tok.ExpiresAt.Equal(now.Add(enrollment.DefaultTTL)) || tok.CreatedBy != ann {
		t.Errorf("defaults: %+v (want 1 use, 1 h)", tok)
	}
	if !store.scope.AllowsGroup(group1) {
		t.Error("the token must be stored in the group's scope")
	}
	if a := store.audits[0]; a.Action != "enrollment_token.create" || a.Details["group"] != group1.String() || a.Details["max_uses"] != "1" {
		t.Errorf("audit = %+v", a)
	}
}

func TestCreate_BoundsLifetimeAndUses(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		ttl  time.Duration
		uses int
		ok   bool
	}{
		{5 * time.Minute, 1, true},
		{7 * 24 * time.Hour, 1000, true},
		{4 * time.Minute, 1, false},
		{8 * 24 * time.Hour, 1, false},
		{time.Hour, 1001, false},
		{time.Hour, -1, false},
	}
	for _, tc := range cases {
		_, _, err := service(&fakeStore{}).Create(ctx, caller(role.Operator, group1), group1, tc.ttl, tc.uses)
		if (err == nil) != tc.ok || (!tc.ok && !errors.Is(err, enrollment.ErrInvalidLimits)) {
			t.Errorf("ttl %v, uses %d: error = %v, ok want %v", tc.ttl, tc.uses, err, tc.ok)
		}
	}
}

func TestCreate_NeedsOperatorOnTheGroup(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]access.Caller{
		"viewer":             caller(role.Viewer, group1),
		"operator elsewhere": caller(role.Operator, group2),
		"no organization":    {Principal: authz.Principal{User: ann}},
	} {
		store := &fakeStore{}
		if _, _, err := service(store).Create(ctx, c, group1, 0, 0); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s: %v, want ErrForbidden", name, err)
		}
		if store.calls != 0 {
			t.Errorf("%s: store called", name)
		}
	}
}

func TestListAndRevoke_UseTheGroupsWithOperatorRights(t *testing.T) {
	store := &fakeStore{}
	c := caller(role.Operator, group1)
	c.Principal.Bindings = append(c.Principal.Bindings, authz.Binding{Group: group2, Role: role.Viewer})
	if _, err := service(store).List(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !store.scope.AllowsGroup(group1) || store.scope.AllowsGroup(group2) {
		t.Errorf("list scope = %+v, want only group1", store.scope)
	}
	id := enrollment.TokenID(uuid.New())
	if err := service(store).Revoke(context.Background(), c, id); err != nil {
		t.Fatal(err)
	}
	if store.scope.AllowsGroup(group2) || store.audits[0].Action != "enrollment_token.revoke" {
		t.Errorf("revoke scope %+v, audit %+v", store.scope, store.audits)
	}
	if _, err := service(&fakeStore{}).List(context.Background(), caller(role.Viewer, group1)); err != nil {
		t.Errorf("a viewer gets an empty list, not an error: %v", err)
	}
}

func TestRedeem_ChecksTheTokenAndConsumesOneUse(t *testing.T) {
	secret, hash := "sme_"+strings.Repeat("A", 43), sha256.Sum256([]byte("sme_"+strings.Repeat("A", 43)))
	stored := &enrollment.Stored{ID: enrollment.TokenID(uuid.New()), Org: orgA, Group: group1, Hash: hash}
	store := &fakeStore{lookup: stored}

	grant, err := service(store).Redeem(context.Background(), secret)
	if err != nil || grant.Org != orgA || grant.Group != group1 || store.consumed != stored.ID {
		t.Fatalf("Redeem() = %+v, %v", grant, err)
	}
	if a := store.audits[0]; a.Action != "enrollment_token.use" || !a.Actor.IsZero() {
		t.Errorf("audit = %+v (system action without actor)", a)
	}

	for name, tc := range map[string]struct {
		secret string
		store  *fakeStore
	}{
		"no prefix":      {strings.Repeat("A", 47), &fakeStore{lookup: stored}},
		"wrong length":   {"sme_short", &fakeStore{lookup: stored}},
		"unknown":        {secret, &fakeStore{}},
		"hash mismatch":  {secret, &fakeStore{lookup: &enrollment.Stored{ID: stored.ID, Org: orgA, Group: group1}}},
		"used up/racing": {secret, &fakeStore{lookup: stored, consume: enrollment.ErrInvalidToken}},
	} {
		if _, err := service(tc.store).Redeem(context.Background(), tc.secret); !errors.Is(err, enrollment.ErrInvalidToken) {
			t.Errorf("%s: %v, want ErrInvalidToken", name, err)
		}
	}
}
