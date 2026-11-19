package access_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	bob    = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	group1 = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
	fixed  = uuid.MustParse("0193f5a0-0000-7000-8000-0000000000f1")
)

// fakeStore records calls and the audit entries the service hands over.
type fakeStore struct {
	err     error
	audits  []access.AuditEntry
	scopes  []tenant.Scope
	groups  []access.Group
	grants  []access.Grant
	limit   int
	before  int64
	members []access.Member
}

func (f *fakeStore) record(scope tenant.Scope, a access.AuditEntry) error {
	f.scopes = append(f.scopes, scope)
	f.audits = append(f.audits, a)
	return f.err
}

func (f *fakeStore) ListGroups(_ context.Context, scope tenant.Scope) ([]access.Group, error) {
	f.scopes = append(f.scopes, scope)
	return f.groups, f.err
}

func (f *fakeStore) CreateGroup(_ context.Context, scope tenant.Scope, g access.Group, a access.AuditEntry) error {
	f.groups = append(f.groups, g)
	return f.record(scope, a)
}

func (f *fakeStore) RenameGroup(_ context.Context, scope tenant.Scope, _ tenant.GroupID, _ string, a access.AuditEntry) error {
	return f.record(scope, a)
}

func (f *fakeStore) DeleteGroup(_ context.Context, scope tenant.Scope, _ tenant.GroupID, a access.AuditEntry) error {
	return f.record(scope, a)
}

func (f *fakeStore) ListMembers(_ context.Context, scope tenant.Scope) ([]access.Member, error) {
	f.scopes = append(f.scopes, scope)
	return f.members, f.err
}

func (f *fakeStore) GrantRole(_ context.Context, scope tenant.Scope, g access.Grant, a access.AuditEntry) (access.BindingID, error) {
	f.grants = append(f.grants, g)
	return g.ID, f.record(scope, a)
}

func (f *fakeStore) RevokeRole(_ context.Context, scope tenant.Scope, _ access.BindingID, a access.AuditEntry) error {
	return f.record(scope, a)
}

func (f *fakeStore) RemoveMember(_ context.Context, scope tenant.Scope, _ identity.UserID, a access.AuditEntry) error {
	return f.record(scope, a)
}

func (f *fakeStore) ListAudit(_ context.Context, scope tenant.Scope, before int64, limit int) ([]access.AuditRecord, error) {
	f.scopes = append(f.scopes, scope)
	f.before, f.limit = before, limit
	return nil, f.err
}

func service(store *fakeStore) *access.Service {
	return access.NewService(store, authz.NewPolicy(), func() time.Time { return now }, func() uuid.UUID { return fixed })
}

func caller(r role.Role, allGroups bool, stepUpAge time.Duration) access.Caller {
	b := authz.Binding{Role: r, AllGroups: allGroups, Group: group1}
	return access.Caller{
		Principal: authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{b}},
		StepUpAt:  now.Add(-stepUpAge),
	}
}

var (
	admin    = caller(role.Admin, true, time.Minute)
	stale    = caller(role.Admin, true, time.Hour) // admin without a recent step-up
	operator = caller(role.Operator, true, time.Minute)
	gAdmin   = caller(role.Admin, false, time.Minute) // admin of one group only, not org-wide
)

func TestManagementRequiresAnOrgWideAdmin(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(*access.Service, access.Caller) error{
		"CreateGroup": func(s *access.Service, c access.Caller) error { _, err := s.CreateGroup(ctx, c, "web"); return err },
		"RenameGroup": func(s *access.Service, c access.Caller) error { return s.RenameGroup(ctx, c, group1, "db") },
		"DeleteGroup": func(s *access.Service, c access.Caller) error { return s.DeleteGroup(ctx, c, group1) },
		"ListMembers": func(s *access.Service, c access.Caller) error { _, err := s.ListMembers(ctx, c); return err },
		"GrantRole": func(s *access.Service, c access.Caller) error {
			_, err := s.GrantRole(ctx, c, bob, nil, role.Viewer)
			return err
		},
		"RevokeRole":   func(s *access.Service, c access.Caller) error { return s.RevokeRole(ctx, c, access.BindingID(fixed)) },
		"RemoveMember": func(s *access.Service, c access.Caller) error { return s.RemoveMember(ctx, c, bob) },
		"ListAudit":    func(s *access.Service, c access.Caller) error { _, err := s.ListAudit(ctx, c, 0, 10); return err },
	}
	for name, call := range calls {
		for _, c := range []access.Caller{operator, gAdmin, {Principal: authz.Principal{User: alice}}} {
			store := &fakeStore{}
			if err := call(service(store), c); !errors.Is(err, access.ErrForbidden) {
				t.Errorf("%s by %+v: error = %v, want ErrForbidden", name, c.Principal.Bindings, err)
			}
			if len(store.scopes) != 0 {
				t.Errorf("%s: store was called although access was denied", name)
			}
		}
		if err := call(service(&fakeStore{}), admin); err != nil {
			t.Errorf("%s by org admin: %v", name, err)
		}
	}
}

func TestRoleChangesRequireARecentStepUp(t *testing.T) {
	ctx := context.Background()
	s := service(&fakeStore{})
	if _, err := s.GrantRole(ctx, stale, bob, nil, role.Viewer); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("GrantRole: %v, want ErrStepUpRequired", err)
	}
	if err := s.RevokeRole(ctx, stale, access.BindingID(fixed)); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("RevokeRole: %v, want ErrStepUpRequired", err)
	}
	if err := s.RemoveMember(ctx, stale, bob); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("RemoveMember: %v, want ErrStepUpRequired", err)
	}
	// Deleting a group removes every role on it.
	if err := s.DeleteGroup(ctx, stale, group1); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("DeleteGroup: %v, want ErrStepUpRequired", err)
	}
	// Creating and renaming groups change no rights: no step-up needed.
	if _, err := s.CreateGroup(ctx, stale, "web"); err != nil {
		t.Errorf("CreateGroup with stale step-up: %v", err)
	}
}

func TestCreateGroup_ValidatesTheNameAndAudits(t *testing.T) {
	store := &fakeStore{}
	s := service(store)

	g, err := s.CreateGroup(context.Background(), admin, "  web servers ")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != tenant.GroupID(fixed) || g.Name != "web servers" {
		t.Errorf("group = %+v", g)
	}
	want := access.AuditEntry{Actor: alice, Action: "group.create", Target: "group:" + fixed.String(), Details: map[string]string{"name": "web servers"}}
	if len(store.audits) != 1 || !equalAudit(store.audits[0], want) {
		t.Errorf("audit = %+v, want %+v", store.audits, want)
	}
	if !store.scopes[0].AllGroups() || store.scopes[0].OrgID() != orgA {
		t.Errorf("scope = %+v, want all groups of the caller's org", store.scopes[0])
	}

	for _, bad := range []string{"", "   ", strings.Repeat("x", 101), "a\nb", "tab\there"} {
		if _, err := s.CreateGroup(context.Background(), admin, bad); !errors.Is(err, access.ErrInvalidName) {
			t.Errorf("CreateGroup(%q) error = %v, want ErrInvalidName", bad, err)
		}
	}
}

func TestGrantRole_AuditsRoleAndGroup(t *testing.T) {
	store := &fakeStore{}
	s := service(store)
	g := group1

	if _, err := s.GrantRole(context.Background(), admin, bob, &g, role.Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantRole(context.Background(), admin, bob, nil, role.Viewer); err != nil {
		t.Fatal(err)
	}
	if store.audits[0].Details["group"] != group1.String() || store.audits[0].Details["role"] != "operator" ||
		store.audits[1].Details["group"] != "*" || store.audits[0].Target != "user:"+bob.String() {
		t.Errorf("audits = %+v", store.audits)
	}
	if store.grants[0].User != bob || *store.grants[0].Group != group1 || store.grants[1].Group != nil {
		t.Errorf("grants = %+v", store.grants)
	}
	if _, err := s.GrantRole(context.Background(), admin, bob, nil, role.Role(0)); !errors.Is(err, access.ErrInvalidRole) {
		t.Errorf("invalid role: %v, want ErrInvalidRole", err)
	}
}

func TestListGroups_UsesTheCallersVisibleGroups(t *testing.T) {
	store := &fakeStore{}
	viewerOfGroup := caller(role.Viewer, false, time.Hour)

	if _, err := service(store).ListGroups(context.Background(), viewerOfGroup); err != nil {
		t.Fatal(err)
	}
	if sc := store.scopes[0]; sc.AllGroups() || !sc.AllowsGroup(group1) || len(sc.Groups()) != 1 {
		t.Errorf("scope = %+v, want only group1", sc)
	}
	if _, err := service(store).ListGroups(context.Background(), access.Caller{Principal: authz.Principal{User: alice}}); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("without organization: %v, want ErrForbidden", err)
	}
}

func TestListAudit_BoundsThePageSize(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 100}, {-1, 100}, {50, 50}, {10_000, 500}} {
		store := &fakeStore{}
		if _, err := service(store).ListAudit(context.Background(), admin, 42, tc.in); err != nil {
			t.Fatal(err)
		}
		if store.limit != tc.want || store.before != 42 {
			t.Errorf("limit %d → %d (before %d), want %d", tc.in, store.limit, store.before, tc.want)
		}
	}
}

func TestStoreErrorsArePropagated(t *testing.T) {
	store := &fakeStore{err: access.ErrLastAdmin}
	if err := service(store).RevokeRole(context.Background(), admin, access.BindingID(fixed)); !errors.Is(err, access.ErrLastAdmin) {
		t.Errorf("error = %v, want ErrLastAdmin", err)
	}
}

func equalAudit(a, b access.AuditEntry) bool {
	if a.Actor != b.Actor || a.Action != b.Action || a.Target != b.Target || len(a.Details) != len(b.Details) {
		return false
	}
	for k, v := range b.Details {
		if a.Details[k] != v {
			return false
		}
	}
	return true
}
