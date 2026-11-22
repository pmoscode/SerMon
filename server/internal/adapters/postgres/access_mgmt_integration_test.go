//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// mgmtOrg is a fresh organization with an org-wide admin and a second member (no roles).
type mgmtOrg struct {
	scope        tenant.Scope
	admin, other identity.UserID
	adminBinding access.BindingID
}

func newMgmtOrg(t *testing.T, f fixture) mgmtOrg {
	t.Helper()
	org, admin, other, binding := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Mgmt ' || $1::uuid::text)", org)
	for _, u := range []uuid.UUID{admin, other} {
		ownerExec(t, f.db.OwnerDSN, "INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'User')", u, u.String()+"@example.org")
		ownerExec(t, f.db.OwnerDSN, "INSERT INTO memberships (org_id, user_id) VALUES ($1, $2)", org, u)
	}
	ownerExec(t, f.db.OwnerDSN, "SELECT set_config('sermon.org_id', $1, false)", org.String())
	ownerExec(t, f.db.OwnerDSN, `INSERT INTO role_bindings (id, org_id, user_id, role) VALUES ($1, $2, $3, 'admin')`, binding, org, admin)
	return mgmtOrg{scope: scopeFor(t, org), admin: identity.UserID(admin), other: identity.UserID(other), adminBinding: access.BindingID(binding)}
}

func entry(actor identity.UserID, action string) access.AuditEntry {
	return access.AuditEntry{Actor: actor, Action: action, Target: "t", Details: map[string]string{"k": "v"}}
}

func auditActions(t *testing.T, s *postgres.AccessStore, scope tenant.Scope) []string {
	t.Helper()
	records, err := s.ListAudit(context.Background(), scope, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for i := len(records) - 1; i >= 0; i-- {
		actions = append(actions, records[i].Action)
	}
	return actions
}

func TestAccessMgmt_GroupLifecycleIsAudited(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewAccessStore(open(t, f.appDSN))
	o := newMgmtOrg(t, f)
	g := access.Group{ID: tenant.GroupID(uuid.New()), Name: "web"}

	if err := s.CreateGroup(ctx, o.scope, g, entry(o.admin, "group.create")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(ctx, o.scope, access.Group{ID: tenant.GroupID(uuid.New()), Name: "web"}, entry(o.admin, "group.create")); !errors.Is(err, access.ErrConflict) {
		t.Errorf("duplicate name: %v, want ErrConflict", err)
	}
	if err := s.RenameGroup(ctx, o.scope, g.ID, "frontend", entry(o.admin, "group.rename")); err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListGroups(ctx, o.scope)
	if err != nil || len(groups) != 1 || groups[0].Name != "frontend" {
		t.Fatalf("ListGroups() = %+v, %v", groups, err)
	}
	if err := s.DeleteGroup(ctx, o.scope, g.ID, entry(o.admin, "group.delete")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(ctx, o.scope, g.ID, entry(o.admin, "group.delete")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("second delete: %v, want ErrNotFound", err)
	}

	// Failed changes leave no audit entry (same transaction).
	want := []string{"group.create", "group.rename", "group.delete"}
	if got := auditActions(t, s, o.scope); !equalStrings(got, want) {
		t.Errorf("audit = %v, want %v", got, want)
	}
	if err := s.VerifyAuditChain(ctx, o.scope); err != nil {
		t.Error(err)
	}
	records, _ := s.ListAudit(ctx, o.scope, 0, 1)
	if len(records) != 1 || records[0].Actor == nil || *records[0].Actor != o.admin || records[0].Details["k"] != "v" {
		t.Errorf("newest record = %+v", records)
	}
}

func TestAccessMgmt_ListGroupsHonorsTheScope(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewAccessStore(open(t, f.appDSN))
	o := newMgmtOrg(t, f)
	a, b := tenant.GroupID(uuid.New()), tenant.GroupID(uuid.New())
	for i, id := range []tenant.GroupID{a, b} {
		if err := s.CreateGroup(ctx, o.scope, access.Group{ID: id, Name: string(rune('a' + i))}, entry(o.admin, "group.create")); err != nil {
			t.Fatal(err)
		}
	}
	only, _ := tenant.ForGroups(o.scope.OrgID(), b)
	none, _ := tenant.ForGroups(o.scope.OrgID())

	if got, err := s.ListGroups(ctx, only); err != nil || len(got) != 1 || got[0].ID != b {
		t.Errorf("ListGroups(only b) = %+v, %v", got, err)
	}
	if got, err := s.ListGroups(ctx, none); err != nil || len(got) != 0 {
		t.Errorf("ListGroups(no groups) = %+v, %v", got, err)
	}
}

func TestAccessMgmt_ForeignObjectsAreNotFound(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewAccessStore(open(t, f.appDSN))
	a, b := newMgmtOrg(t, f), newMgmtOrg(t, f)
	groupA := tenant.GroupID(uuid.New())
	if err := s.CreateGroup(ctx, a.scope, access.Group{ID: groupA, Name: "a"}, entry(a.admin, "group.create")); err != nil {
		t.Fatal(err)
	}

	checks := map[string]error{
		"rename foreign group":     s.RenameGroup(ctx, b.scope, groupA, "x", entry(b.admin, "x")),
		"delete foreign group":     s.DeleteGroup(ctx, b.scope, groupA, entry(b.admin, "x")),
		"revoke foreign binding":   s.RevokeRole(ctx, b.scope, a.adminBinding, entry(b.admin, "x")),
		"remove foreign member":    s.RemoveMember(ctx, b.scope, a.other, entry(b.admin, "x")),
		"grant to a foreign user":  grantErr(s, b.scope, access.Grant{ID: access.BindingID(uuid.New()), User: a.other, Role: role.Viewer}, b.admin),
		"grant on a foreign group": grantErr(s, b.scope, access.Grant{ID: access.BindingID(uuid.New()), User: b.other, Group: &groupA, Role: role.Viewer}, b.admin),
	}
	for name, err := range checks {
		if !errors.Is(err, access.ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if got := auditActions(t, s, b.scope); len(got) != 0 {
		t.Errorf("failed foreign changes were audited: %v", got)
	}
	members, err := s.ListMembers(ctx, b.scope)
	if err != nil || len(members) != 2 {
		t.Errorf("ListMembers(b) = %+v, %v; want only b's two members", members, err)
	}
}

func grantErr(s *postgres.AccessStore, scope tenant.Scope, g access.Grant, actor identity.UserID) error {
	_, err := s.GrantRole(context.Background(), scope, g, entry(actor, "role.grant"))
	return err
}

func TestAccessMgmt_GrantReplacesTheRoleOnTheSameGroup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewAccessStore(open(t, f.appDSN))
	o := newMgmtOrg(t, f)

	first, err := s.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.other, Role: role.Viewer}, entry(o.admin, "role.grant"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.other, Role: role.Operator}, entry(o.admin, "role.grant"))
	if err != nil || second != first {
		t.Fatalf("second grant = %v, %v; want the same binding %v", second, err, first)
	}
	members, _ := s.ListMembers(ctx, o.scope)
	for _, m := range members {
		if m.User == o.other && (len(m.Bindings) != 1 || m.Bindings[0].Role != role.Operator || m.Bindings[0].Group != nil) {
			t.Errorf("bindings of other = %+v, want one operator on all groups", m.Bindings)
		}
	}
}

func TestAccessMgmt_TheLastOrgAdminCannotBeRemoved(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewAccessStore(open(t, f.appDSN))
	o := newMgmtOrg(t, f)

	if err := s.RevokeRole(ctx, o.scope, o.adminBinding, entry(o.admin, "role.revoke")); !errors.Is(err, access.ErrLastAdmin) {
		t.Errorf("revoke last admin: %v, want ErrLastAdmin", err)
	}
	if err := s.RemoveMember(ctx, o.scope, o.admin, entry(o.admin, "member.remove")); !errors.Is(err, access.ErrLastAdmin) {
		t.Errorf("remove last admin: %v, want ErrLastAdmin", err)
	}
	if _, err := s.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.admin, Role: role.Viewer}, entry(o.admin, "role.grant")); !errors.Is(err, access.ErrLastAdmin) {
		t.Errorf("downgrade last admin: %v, want ErrLastAdmin", err)
	}

	// With a second org admin the first one can go.
	if _, err := s.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.other, Role: role.Admin}, entry(o.admin, "role.grant")); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeRole(ctx, o.scope, o.adminBinding, entry(o.admin, "role.revoke")); err != nil {
		t.Errorf("revoke with a second admin: %v", err)
	}
	if got, want := auditActions(t, s, o.scope), []string{"role.grant", "role.revoke"}; !equalStrings(got, want) {
		t.Errorf("audit = %v, want %v", got, want)
	}
	records, _ := s.ListAudit(ctx, o.scope, 0, 1)
	if records[0].Details["role"] != "admin" || records[0].Details["group"] != "*" || records[0].Details["user"] != o.admin.String() {
		t.Errorf("revoke audit details = %v, want role, group and user of the binding", records[0].Details)
	}
}

func TestAccessMgmt_ConcurrentRevocationsKeepOneAdmin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewAccessStore(open(t, f.appDSN))
	o := newMgmtOrg(t, f)
	second, err := s.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.other, Role: role.Admin}, entry(o.admin, "role.grant"))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []access.BindingID{o.adminBinding, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.RevokeRole(ctx, o.scope, id, entry(o.admin, "role.revoke"))
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Errorf("errors = %v; want exactly one revocation to succeed", errs)
	}
}

func TestAccessMgmt_RemoveMemberEndsItsSessionsInThatOrganization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	s := postgres.NewAccessStore(db)
	o := newMgmtOrg(t, f)
	sessions := postgres.NewSessionStore(db)
	now := time.Now()
	inOrg := sessionFor(o.other, o.scope.OrgID(), now)
	elsewhere := sessionFor(o.other, tenant.OrgID{}, now)
	for _, se := range []session.Session{inOrg, elsewhere} {
		if err := sessions.Insert(ctx, se); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.RemoveMember(ctx, o.scope, o.other, entry(o.admin, "member.remove")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := sessions.Get(ctx, inOrg.ID); found {
		t.Error("session in the organization still exists")
	}
	if _, found, _ := sessions.Get(ctx, elsewhere.ID); !found {
		t.Error("session outside the organization was deleted")
	}
	if err := s.RemoveMember(ctx, o.scope, o.other, entry(o.admin, "member.remove")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("second removal: %v, want ErrNotFound", err)
	}
}

func TestAccessMgmt_ListAuditPages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewAccessStore(open(t, f.appDSN))
	o := newMgmtOrg(t, f)
	for i := range 5 {
		if err := s.CreateGroup(ctx, o.scope, access.Group{ID: tenant.GroupID(uuid.New()), Name: string(rune('a' + i))}, entry(o.admin, "group.create")); err != nil {
			t.Fatal(err)
		}
	}
	page1, _ := s.ListAudit(ctx, o.scope, 0, 2)
	page2, _ := s.ListAudit(ctx, o.scope, page1[1].Seq, 2)
	if len(page1) != 2 || len(page2) != 2 || page1[0].Seq != 5 || page2[0].Seq != 3 || page2[1].Seq != 2 {
		t.Errorf("pages = %+v / %+v", page1, page2)
	}
}

func sessionFor(user identity.UserID, org tenant.OrgID, now time.Time) session.Session {
	id := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	return session.Session{ID: id, User: user, Org: org, CreatedAt: now, LastSeenAt: now, StepUpAt: now}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
