package oidcmapping_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/oidcmapping"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	t0     = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	alice  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	web    = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
	db     = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c2"))
	bindID = uuid.MustParse("0193f5a0-0000-7000-8000-0000000000d1")
)

func g(id tenant.GroupID) *tenant.GroupID { return &id }

func mapping(group string, on *tenant.GroupID, r role.Role) oidcmapping.Mapping {
	return oidcmapping.Mapping{ID: oidcmapping.ID(uuid.New()), OIDCGroup: group, Group: on, Role: r}
}

func TestDesired_TheHighestRolePerNodeGroup(t *testing.T) {
	mappings := []oidcmapping.Mapping{
		mapping("ops", nil, role.Operator),
		mapping("admins", nil, role.Admin),
		mapping("ops", g(web), role.Viewer),
		mapping("dba", g(db), role.Operator),
		mapping("dba", g(db), role.Viewer),
	}
	d := oidcmapping.Desired(mappings, []string{"ops", "dba", "unmapped"})
	if len(d) != 3 || d["*"].Role != role.Operator || d[web.String()].Role != role.Viewer || d[db.String()].Role != role.Operator {
		t.Errorf("desired = %+v", d)
	}
	if d := oidcmapping.Desired(mappings, []string{"ops", "admins"}); d["*"].Role != role.Admin {
		t.Errorf("both org-wide: %+v", d)
	}
	if d := oidcmapping.Desired(mappings, nil); len(d) != 0 {
		t.Errorf("no groups: %+v", d)
	}
	// Group names compare exactly.
	if d := oidcmapping.Desired(mappings, []string{"OPS", " ops"}); len(d) != 0 {
		t.Errorf("other spellings: %+v", d)
	}
	// Only the first MaxClaimGroups groups count.
	many := make([]string, oidcmapping.MaxClaimGroups)
	for i := range many {
		many[i] = "x"
	}
	if d := oidcmapping.Desired(mappings, append(many, "ops")); len(d) != 0 {
		t.Errorf("beyond the bound: %+v", d)
	}
}

func TestPlan_AddsTakesAndLeavesManualRoles(t *testing.T) {
	manual := uuid.New()
	fromOIDC := uuid.New()
	stale := uuid.New()
	existing := []oidcmapping.Binding{
		{ID: manual, Group: g(web), Role: role.Admin},
		{ID: fromOIDC, Group: nil, Role: role.Viewer, FromOIDC: true},
		{ID: stale, Group: g(db), Role: role.Operator, FromOIDC: true},
	}
	desired := oidcmapping.Desired([]oidcmapping.Mapping{
		mapping("ops", nil, role.Operator),
		mapping("ops", g(web), role.Viewer), // web has a role by hand: it stays
	}, []string{"ops"})
	changes := oidcmapping.Plan(existing, desired)
	want := []oidcmapping.Change{
		{Kind: oidcmapping.Update, Binding: fromOIDC, Role: role.Operator},
		{Kind: oidcmapping.Remove, Binding: stale, Group: g(db), Role: role.Operator},
	}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v", changes)
	}
	for i, c := range changes {
		if c.Kind != want[i].Kind || c.Binding != want[i].Binding || c.Role != want[i].Role {
			t.Errorf("change %d = %+v, want %+v", i, c, want[i])
		}
	}
	// A new member gets every role; a member without mapped groups loses only the mapped ones.
	if add := oidcmapping.Plan(nil, desired); len(add) != 2 || add[0].Kind != oidcmapping.Add || add[1].Kind != oidcmapping.Add {
		t.Errorf("new member: %+v", add)
	}
	gone := oidcmapping.Plan(existing, nil)
	if len(gone) != 2 || slices.ContainsFunc(gone, func(c oidcmapping.Change) bool { return c.Binding == manual }) {
		t.Errorf("no groups: %+v", gone)
	}
	// Same role: nothing to do.
	if same := oidcmapping.Plan([]oidcmapping.Binding{{ID: fromOIDC, Role: role.Operator, FromOIDC: true}}, desired); len(same) != 1 || same[0].Kind != oidcmapping.Add {
		t.Errorf("unchanged org-wide role: %+v", same)
	}
}

type store struct {
	mappings []oidcmapping.Mapping
	audits   []access.AuditEntry
	scope    tenant.Scope
	mapped   []tenant.OrgID
	asked    []string
	plans    map[tenant.OrgID][]oidcmapping.Change
	joins    map[tenant.OrgID]bool
	existing []oidcmapping.Binding
	provErr  error
	created  []string
}

func (s *store) List(_ context.Context, scope tenant.Scope) ([]oidcmapping.Mapping, error) {
	s.scope = scope
	return s.mappings, nil
}

func (s *store) Create(_ context.Context, scope tenant.Scope, m oidcmapping.Mapping, audit access.AuditEntry) error {
	s.scope = scope
	s.mappings = append(s.mappings, m)
	s.audits = append(s.audits, audit)
	return nil
}

func (s *store) Delete(_ context.Context, scope tenant.Scope, _ oidcmapping.ID, audit access.AuditEntry) error {
	s.scope = scope
	s.audits = append(s.audits, audit)
	return nil
}

func (s *store) MappedOrganizations(_ context.Context, groups []string) ([]tenant.OrgID, error) {
	s.asked = groups
	return s.mapped, nil
}

func (s *store) Reconcile(_ context.Context, scope tenant.Scope, _ identity.UserID, join bool,
	plan func([]oidcmapping.Mapping, []oidcmapping.Binding) []oidcmapping.Change,
) (oidcmapping.OrgResult, error) {
	if s.plans == nil {
		s.plans, s.joins = map[tenant.OrgID][]oidcmapping.Change{}, map[tenant.OrgID]bool{}
	}
	s.plans[scope.OrgID()] = plan(s.mappings, s.existing)
	s.joins[scope.OrgID()] = join
	return oidcmapping.OrgResult{}, nil
}

func (s *store) Provision(_ context.Context, email, name string, _ oidc.Identity) (identity.UserID, error) {
	if s.provErr != nil {
		return identity.UserID{}, s.provErr
	}
	s.created = append(s.created, email+"|"+name)
	return alice, nil
}

type members []tenant.OrgID

func (m members) Organizations(context.Context, identity.UserID) ([]auth.Organization, error) {
	out := make([]auth.Organization, len(m))
	for i, o := range m {
		out[i] = auth.Organization{ID: o}
	}
	return out, nil
}

func service(s *store, orgs members) *oidcmapping.Service {
	return oidcmapping.NewService(s, orgs, authz.NewPolicy(), func() time.Time { return t0 }, uuid.New, nil)
}

func caller(r role.Role, stepUp time.Time) access.Caller {
	return access.Caller{Principal: authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: r}}}, StepUpAt: stepUp}
}

// Admins for all groups keep the mappings; changes need a step-up and are audited.
func TestService_OnlyAdminsWithAStepUp(t *testing.T) {
	ctx := context.Background()
	s := &store{}
	svc := service(s, nil)
	for _, r := range []role.Role{role.Viewer, role.Operator} {
		if _, err := svc.List(ctx, caller(r, t0)); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s lists: %v", r, err)
		}
		if _, err := svc.Create(ctx, caller(r, t0), "ops", nil, role.Viewer); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s creates: %v", r, err)
		}
	}
	groupAdmin := access.Caller{Principal: authz.Principal{User: alice, Org: orgA, Bindings: []authz.Binding{{Group: web, Role: role.Admin}}}, StepUpAt: t0}
	if _, err := svc.Create(ctx, groupAdmin, "ops", g(web), role.Viewer); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("an admin of one group: %v", err)
	}
	if _, err := svc.List(ctx, caller(role.Admin, time.Time{})); err != nil || s.scope.OrgID() != orgA {
		t.Errorf("admin lists without a step-up: %v", err)
	}
	if _, err := svc.Create(ctx, caller(role.Admin, t0.Add(-time.Hour)), "ops", nil, role.Viewer); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("an old step-up creates: %v", err)
	}
	if err := svc.Delete(ctx, caller(role.Admin, t0.Add(-time.Hour)), oidcmapping.ID(uuid.New())); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("an old step-up deletes: %v", err)
	}
	m, err := svc.Create(ctx, caller(role.Admin, t0), "ops", g(web), role.Operator)
	if err != nil || m.OIDCGroup != "ops" || *m.Group != web || m.Role != role.Operator || !m.CreatedAt.Equal(t0) {
		t.Fatalf("Create() = %+v, %v", m, err)
	}
	if err := svc.Delete(ctx, caller(role.Admin, t0), m.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.audits) != 2 || s.audits[0].Action != "oidc_mapping.create" || s.audits[0].Details["oidc_group"] != "ops" ||
		s.audits[0].Details["role"] != "operator" || s.audits[0].Details["group"] != web.String() ||
		s.audits[1].Action != "oidc_mapping.delete" || s.audits[1].Target != "oidc_mapping:"+m.ID.String() || s.audits[0].Actor != alice {
		t.Errorf("audits = %+v", s.audits)
	}
	for name, group := range map[string]string{
		"empty": "", "control": "ops\n", "outer space": " ops", "long": strings.Repeat("x", 256), "invalid utf-8": "\xff",
	} {
		if _, err := svc.Create(ctx, caller(role.Admin, t0), group, nil, role.Viewer); !errors.Is(err, oidcmapping.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.Create(ctx, caller(role.Admin, t0), "ops", nil, role.Role(0)); !errors.Is(err, access.ErrInvalidRole) {
		t.Errorf("no role: %v", err)
	}
	if _, err := svc.Create(ctx, caller(role.Admin, t0), "CN=Ops,OU=Groups/äöü", nil, role.Viewer); err != nil {
		t.Errorf("a distinguished name: %v", err)
	}
}

// A sign-in reconciles in the user's organizations, and with join also in those whose mappings
// match its groups.
func TestReconcile_TheOrganizationsOfTheUserAndTheMapped(t *testing.T) {
	ctx := context.Background()
	s := &store{mappings: []oidcmapping.Mapping{mapping("ops", nil, role.Operator)}, mapped: []tenant.OrgID{orgA, orgB}}
	svc := service(s, members{orgA})
	if err := svc.Reconcile(ctx, alice, []string{"ops"}, false); err != nil {
		t.Fatal(err)
	}
	if len(s.plans) != 1 || len(s.plans[orgA]) != 1 || s.plans[orgA][0].Kind != oidcmapping.Add || s.joins[orgA] || s.asked != nil {
		t.Errorf("without join: plans %+v, asked %v", s.plans, s.asked)
	}
	s.plans = nil
	if err := svc.Reconcile(ctx, alice, []string{"ops"}, true); err != nil {
		t.Fatal(err)
	}
	if len(s.plans) != 2 || !s.joins[orgB] || strings.Join(s.asked, ",") != "ops" {
		t.Errorf("with join: plans %+v, asked %v", s.plans, s.asked)
	}
	// Without groups nobody joins, the mapped roles go.
	s.plans, s.asked = nil, nil
	s.existing = []oidcmapping.Binding{{ID: bindID, Role: role.Operator, FromOIDC: true}}
	if err := svc.Reconcile(ctx, alice, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(s.plans) != 1 || len(s.plans[orgA]) != 1 || s.plans[orgA][0].Kind != oidcmapping.Remove || s.asked != nil {
		t.Errorf("no groups: plans %+v", s.plans)
	}
}

func TestProvision_OnlyWithAVerifiedAddressAndAMapping(t *testing.T) {
	ctx := context.Background()
	claims := oidc.Claims{Issuer: "https://idp.example", Subject: "s", Email: "new@example.org", EmailVerified: true, Name: " New User ", Groups: []string{"ops"}}
	s := &store{mapped: []tenant.OrgID{orgA}}
	svc := service(s, nil)
	user, ok, err := svc.Provision(ctx, claims)
	if err != nil || !ok || user != alice || len(s.created) != 1 || s.created[0] != "new@example.org|New User" {
		t.Fatalf("Provision() = %v, %v, %v, %v", user, ok, err, s.created)
	}
	for name, change := range map[string]func(c *oidc.Claims){
		"unverified": func(c *oidc.Claims) { c.EmailVerified = false },
		"no e-mail":  func(c *oidc.Claims) { c.Email = "" },
		"no groups":  func(c *oidc.Claims) { c.Groups = nil },
	} {
		c := claims
		change(&c)
		s.created = nil
		if _, ok, err := svc.Provision(ctx, c); ok || err != nil || len(s.created) != 0 {
			t.Errorf("%s: %v, %v", name, ok, err)
		}
	}
	s.mapped = nil
	if _, ok, _ := svc.Provision(ctx, claims); ok {
		t.Error("no organization maps the groups")
	}
	s.mapped = []tenant.OrgID{orgA}
	nameless := claims
	nameless.Name = ""
	s.created = nil
	if _, ok, _ := svc.Provision(ctx, nameless); !ok || s.created[0] != "new@example.org|new" {
		t.Errorf("without a name: %v", s.created)
	}
	// M3-28: names with bidi or invisible characters are refused like everywhere else.
	for _, hostile := range []string{"\u202Enimda.ti", "a\u200Bdmin", "line\u2028break"} {
		c := claims
		c.Name = hostile
		s.created = nil
		if _, ok, _ := svc.Provision(ctx, c); !ok || s.created[0] != "new@example.org|new" {
			t.Errorf("name %q: %v", hostile, s.created)
		}
	}
	odd := claims
	odd.Name, odd.Email = "", "\u202E@example.org"
	s.created = nil
	if _, ok, _ := svc.Provision(ctx, odd); !ok || s.created[0] != "\u202E@example.org|SSO" {
		t.Errorf("neither name nor local part: %v", s.created)
	}
	// An account has the e-mail: it is not taken over, the identity must be linked.
	s.provErr = oidcmapping.ErrEmailTaken
	if _, ok, err := svc.Provision(ctx, claims); ok || err != nil {
		t.Errorf("taken e-mail: %v, %v", ok, err)
	}
}
