//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/oidcmapping"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type mappingEnv struct {
	f      fixture
	store  *postgres.OIDCMappingStore
	access *postgres.AccessStore
	svc    *oidcmapping.Service
}

func newMappingEnv(t *testing.T) mappingEnv {
	t.Helper()
	f := setup(t)
	db := open(t, f.appDSN)
	store := postgres.NewOIDCMappingStore(db)
	svc := oidcmapping.NewService(store, postgres.NewCredentialStore(db), authz.NewPolicy(), time.Now, uuid.New, nil)
	return mappingEnv{f: f, store: store, access: postgres.NewAccessStore(db), svc: svc}
}

func (e mappingEnv) mapGroup(t *testing.T, scope tenant.Scope, oidcGroup string, group *tenant.GroupID, r role.Role) {
	t.Helper()
	m := oidcmapping.Mapping{ID: oidcmapping.ID(uuid.New()), OIDCGroup: oidcGroup, Group: group, Role: r, CreatedAt: time.Now()}
	if err := e.store.Create(context.Background(), scope, m, access.AuditEntry{Action: "oidc_mapping.create", Target: "oidc_mapping:" + m.ID.String()}); err != nil {
		t.Fatal(err)
	}
}

// bindings returns "group:role:origin" of the user's bindings.
func (e mappingEnv) bindings(t *testing.T, org tenant.OrgID, user identity.UserID) []string {
	t.Helper()
	var out []string
	ownerQueryRow(t, e.f.db.OwnerDSN, `SELECT coalesce(array_agg(b ORDER BY b), '{}') FROM (
		SELECT coalesce(group_id::text, '*') || ':' || role || ':' || origin AS b FROM role_bindings WHERE org_id = $1 AND user_id = $2) x`, []any{uuid.UUID(org), uuid.UUID(user)}, &out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func newGroup(t *testing.T, f fixture, org tenant.OrgID) tenant.GroupID {
	t.Helper()
	id := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, $3)", id, uuid.UUID(org), "g-"+id.String()[:8])
	return tenant.GroupID(id)
}

// M3-21: the sign-in gives and takes the mapped roles, the ones given by hand stay.
func TestOIDCMapping_ReconcileAddsRemovesAndKeepsManualRoles(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	org := o.scope.OrgID()
	web, dbg := newGroup(t, e.f, org), newGroup(t, e.f, org)
	e.mapGroup(t, o.scope, "ops", nil, role.Operator)
	e.mapGroup(t, o.scope, "dba", &dbg, role.Admin)
	// The other member has a viewer role on web by hand.
	if _, err := e.access.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.other, Group: &web, Role: role.Viewer}, entry(o.admin, "role.grant")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reconcile(ctx, o.other, []string{"ops", "dba"}, false); err != nil {
		t.Fatal(err)
	}
	want := []string{"*:operator:oidc", dbg.String() + ":admin:oidc", web.String() + ":viewer:manual"}
	slices.Sort(want)
	if got := e.bindings(t, org, o.other); !slices.Equal(got, want) {
		t.Errorf("after the sign-in: %v, want %v", got, want)
	}
	// Out of "dba": that role goes, the others stay.
	if err := e.svc.Reconcile(ctx, o.other, []string{"ops"}, false); err != nil {
		t.Fatal(err)
	}
	want = []string{"*:operator:oidc", web.String() + ":viewer:manual"}
	if got := e.bindings(t, org, o.other); !slices.Equal(got, want) {
		t.Errorf("out of dba: %v, want %v", got, want)
	}
	// An admin takes over the org-wide role by hand: from now on it stays.
	if _, err := e.access.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.other, Role: role.Viewer}, entry(o.admin, "role.grant")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reconcile(ctx, o.other, nil, false); err != nil {
		t.Fatal(err)
	}
	want = []string{"*:viewer:manual", web.String() + ":viewer:manual"}
	if got := e.bindings(t, org, o.other); !slices.Equal(got, want) {
		t.Errorf("taken over by hand: %v, want %v", got, want)
	}
	// Each change is in the organization's audit log, without an actor, marked as from OIDC.
	var n int
	ownerQueryRow(t, e.f.db.OwnerDSN, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND actor_id IS NULL
		AND action IN ('role.grant', 'role.revoke') AND details->>'origin' = 'oidc'`, []any{uuid.UUID(org)}, &n)
	if n != 3 {
		t.Errorf("%d audit entries of the sign-ins, want 3 (two grants, one revoke)", n)
	}
	if err := e.store.Delete(ctx, o.scope, oidcmapping.ID(uuid.New()), access.AuditEntry{Action: "oidc_mapping.delete"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("an unknown mapping: %v", err)
	}
}

// The org keeps its last org-wide admin even if the provider takes the group away.
func TestOIDCMapping_TheLastAdminStays(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	org := o.scope.OrgID()
	e.mapGroup(t, o.scope, "admins", nil, role.Admin)
	if err := e.svc.Reconcile(ctx, o.other, []string{"admins"}, false); err != nil {
		t.Fatal(err)
	}
	// The hand-given admin leaves; the mapped one is the last.
	if err := e.access.RevokeRole(ctx, o.scope, o.adminBinding, entry(o.admin, "role.revoke")); err != nil {
		t.Fatal(err)
	}
	for _, groups := range [][]string{nil, {"ops"}} {
		if err := e.svc.Reconcile(ctx, o.other, groups, false); err != nil {
			t.Fatal(err)
		}
		if got := e.bindings(t, org, o.other); !slices.Equal(got, []string{"*:admin:oidc"}) {
			t.Errorf("groups %v: %v, want the last admin kept", groups, got)
		}
	}
	// With a second admin it goes.
	if _, err := e.access.GrantRole(ctx, o.scope, access.Grant{ID: access.BindingID(uuid.New()), User: o.admin, Role: role.Admin}, entry(o.admin, "role.grant")); err != nil {
		t.Fatal(err)
	}
	e.mapGroup(t, o.scope, "ops", nil, role.Viewer)
	if err := e.svc.Reconcile(ctx, o.other, []string{"ops"}, false); err != nil {
		t.Fatal(err)
	}
	if got := e.bindings(t, org, o.other); !slices.Equal(got, []string{"*:viewer:oidc"}) {
		t.Errorf("with another admin: %v", got)
	}
}

// JIT: an account for a new identity, membership by the mappings within the quota, never in
// locked organizations; mappings of one organization don't reach into another.
func TestOIDCMapping_ProvisionJoinsWithinQuotaAndNotLocked(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	open1, full, locked, other := newMgmtOrg(t, e.f), newMgmtOrg(t, e.f), newMgmtOrg(t, e.f), newMgmtOrg(t, e.f)
	group := "staff-" + uuid.NewString()
	for _, o := range []mgmtOrg{open1, full, locked} {
		e.mapGroup(t, o.scope, group, nil, role.Viewer)
	}
	e.mapGroup(t, other.scope, "other-"+uuid.NewString(), nil, role.Admin)
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE organizations SET max_members = 2 WHERE id = $1", uuid.UUID(full.scope.OrgID()))
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE organizations SET locked_at = now() WHERE id = $1", uuid.UUID(locked.scope.OrgID()))

	mapped, err := e.store.MappedOrganizations(ctx, []string{group, "unrelated"})
	if err != nil || len(mapped) != 2 || !slices.Contains(mapped, open1.scope.OrgID()) || !slices.Contains(mapped, full.scope.OrgID()) {
		t.Fatalf("MappedOrganizations() = %v, %v; want the two unlocked ones", mapped, err)
	}
	iss := "https://idp-" + uuid.NewString() + ".example"
	email := uuid.NewString() + "@idp.example"
	claims := oidc.Claims{Issuer: iss, Subject: "new-sub", Email: email, EmailVerified: true, Name: "New", Groups: []string{group}}
	user, ok, err := e.svc.Provision(ctx, claims)
	if err != nil || !ok {
		t.Fatalf("Provision() = %v, %v", ok, err)
	}
	if err := e.svc.Reconcile(ctx, user, claims.Groups, true); err != nil {
		t.Fatal(err)
	}
	if got := e.bindings(t, open1.scope.OrgID(), user); !slices.Equal(got, []string{"*:viewer:oidc"}) {
		t.Errorf("open organization: %v", got)
	}
	for name, o := range map[string]mgmtOrg{"full": full, "locked": locked, "other": other} {
		var members int
		ownerQueryRow(t, e.f.db.OwnerDSN, "SELECT count(*) FROM memberships WHERE org_id = $1 AND user_id = $2", []any{uuid.UUID(o.scope.OrgID()), uuid.UUID(user)}, &members)
		if members != 0 {
			t.Errorf("%s organization: joined", name)
		}
	}
	var joins, provisioned int
	ownerQueryRow(t, e.f.db.OwnerDSN, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'member.join' AND details->>'origin' = 'oidc'`,
		[]any{uuid.UUID(open1.scope.OrgID())}, &joins)
	ownerQueryRow(t, e.f.db.OwnerDSN, `SELECT count(*) FROM instance_audit_log WHERE action = 'oidc.user_provisioned' AND details->>'issuer' = $1`, []any{iss}, &provisioned)
	if joins != 1 || provisioned != 1 {
		t.Errorf("%d joins, %d provisioned in the audit logs", joins, provisioned)
	}
	// The identity signs in as the new account.
	if found, ok, _ := postgres.NewOIDCStore(open(t, e.f.appDSN)).Find(ctx, iss, "new-sub"); !ok || found != user {
		t.Errorf("Find() = %v, %v", found, ok)
	}
	// The e-mail has an account now: a second identity with it gets none.
	again := claims
	again.Subject = "another-sub"
	if _, ok, err := e.svc.Provision(ctx, again); ok || err != nil {
		t.Errorf("a second account for the e-mail: %v, %v", ok, err)
	}
	// Without join nobody joins: the locked organization is unlocked, still nothing.
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE organizations SET locked_at = NULL WHERE id = $1", uuid.UUID(locked.scope.OrgID()))
	if err := e.svc.Reconcile(ctx, user, claims.Groups, false); err != nil {
		t.Fatal(err)
	}
	if got := e.bindings(t, locked.scope.OrgID(), user); len(got) != 0 {
		t.Errorf("joined without JIT: %v", got)
	}
	// Joining needs a role to give: nothing to add, no membership.
	res, err := e.store.Reconcile(ctx, other.scope, user, true, func([]oidcmapping.Mapping, []oidcmapping.Binding) []oidcmapping.Change { return nil })
	if err != nil || res.Joined || res.Skipped != "not_member" {
		t.Errorf("join without roles: %+v, %v", res, err)
	}
	// A locked organization keeps the roles as they are while it is locked.
	ownerExec(t, e.f.db.OwnerDSN, "UPDATE organizations SET locked_at = now() WHERE id = $1", uuid.UUID(open1.scope.OrgID()))
	res, err = e.store.Reconcile(ctx, open1.scope, user, true, func([]oidcmapping.Mapping, []oidcmapping.Binding) []oidcmapping.Change {
		return []oidcmapping.Change{{Kind: oidcmapping.Remove}}
	})
	if err != nil || res.Skipped != "locked" || len(res.Applied) != 0 {
		t.Errorf("locked: %+v, %v", res, err)
	}
}

// A mapping is the organization's: duplicates, foreign node groups and the bound are refused.
func TestOIDCMapping_CreateChecks(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	a, b := newMgmtOrg(t, e.f), newMgmtOrg(t, e.f)
	foreign := newGroup(t, e.f, b.scope.OrgID())
	e.mapGroup(t, a.scope, "ops", nil, role.Viewer)
	m := func(group string, on *tenant.GroupID) oidcmapping.Mapping {
		return oidcmapping.Mapping{ID: oidcmapping.ID(uuid.New()), OIDCGroup: group, Group: on, Role: role.Viewer, CreatedAt: time.Now()}
	}
	if err := e.store.Create(ctx, a.scope, m("ops", nil), access.AuditEntry{Action: "x"}); !errors.Is(err, oidcmapping.ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	if err := e.store.Create(ctx, a.scope, m("ops", &foreign), access.AuditEntry{Action: "x"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a node group of another organization: %v", err)
	}
	if list, err := e.store.List(ctx, b.scope); err != nil || len(list) != 0 {
		t.Errorf("the other organization sees %v, %v", list, err)
	}
	for i := 1; i < oidcmapping.MaxMappings; i++ {
		e.mapGroup(t, a.scope, "g"+uuid.NewString(), nil, role.Viewer)
	}
	if err := e.store.Create(ctx, a.scope, m("one-more", nil), access.AuditEntry{Action: "x"}); !errors.Is(err, oidcmapping.ErrTooMany) {
		t.Errorf("beyond the bound: %v", err)
	}
}
