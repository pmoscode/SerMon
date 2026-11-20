package authz_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	groupY = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
	userID = authz.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
)

func member(bindings ...authz.Binding) authz.Principal {
	return authz.Principal{User: userID, Org: orgA, Bindings: bindings}
}

// The role matrix from KONZEPT §7: which role (on the target group or "*") allows which action.
// "-" means no role is sufficient on a single group (org-wide actions need Admin on "*").
var matrix = []struct {
	action  authz.Action
	minimum role.Role // minimum role; 0 = never via a single group binding
	orgWide bool
}{
	{authz.ViewNodes, role.Viewer, false},
	{authz.ViewLogs, role.Viewer, false},
	{authz.ViewSecurityEvents, role.Viewer, false},
	{authz.UseLiveView, role.Viewer, false},
	{authz.AcknowledgeAlerts, role.Operator, false},
	{authz.SilenceAlerts, role.Operator, false},
	{authz.RunUpdates, role.Operator, false},
	{authz.RebootNodes, role.Operator, false},
	{authz.CreateEnrollmentTokens, role.Operator, false},
	{authz.RotateNodeKeys, role.Operator, false},
	{authz.ManageAlertRules, role.Admin, true},
	{authz.ManageChannels, role.Admin, true},
	{authz.ManagePolicies, role.Admin, true},
	{authz.ManageModules, role.Admin, true},
	{authz.ManageMembers, role.Admin, true},
	{authz.ManageGroups, role.Admin, true},
	{authz.ViewAuditLog, role.Admin, true},
}

func TestPolicy_RoleMatrix(t *testing.T) {
	policy := authz.NewPolicy()
	for _, row := range matrix {
		for _, held := range []role.Role{role.Viewer, role.Operator, role.Admin} {
			target := authz.InGroup(orgA, groupX)
			if row.orgWide {
				target = authz.InOrg(orgA)
			}

			allGroups := member(authz.Binding{AllGroups: true, Role: held})
			wantAll := held.AtLeast(row.minimum)
			if got := policy.Allowed(allGroups, row.action, target); got != wantAll {
				t.Errorf("%s with %s on *: Allowed = %v, want %v", row.action, held, got, wantAll)
			}

			oneGroup := member(authz.Binding{Group: groupX, Role: held})
			wantGroup := !row.orgWide && held.AtLeast(row.minimum)
			if got := policy.Allowed(oneGroup, row.action, target); got != wantGroup {
				t.Errorf("%s with %s on group X: Allowed = %v, want %v", row.action, held, got, wantGroup)
			}
		}
	}
}

func TestPolicy_EveryActionIsInTheMatrix(t *testing.T) {
	covered := map[authz.Action]bool{}
	for _, row := range matrix {
		covered[row.action] = true
	}
	for _, action := range authz.AllActions() {
		if !covered[action] {
			t.Errorf("action %s is not covered by the role matrix test", action)
		}
	}
}

func TestPolicy_BindingOnAnotherGroupGrantsNothing(t *testing.T) {
	p := member(authz.Binding{Group: groupY, Role: role.Admin})

	if authz.NewPolicy().Allowed(p, authz.ViewNodes, authz.InGroup(orgA, groupX)) {
		t.Error("admin of group Y must not see nodes of group X")
	}
}

func TestPolicy_HighestBindingWins(t *testing.T) {
	p := member(authz.Binding{AllGroups: true, Role: role.Viewer}, authz.Binding{Group: groupX, Role: role.Operator})
	policy := authz.NewPolicy()

	if !policy.Allowed(p, authz.RunUpdates, authz.InGroup(orgA, groupX)) {
		t.Error("operator binding on group X must allow updates in X")
	}
	if policy.Allowed(p, authz.RunUpdates, authz.InGroup(orgA, groupY)) {
		t.Error("viewer on * must not allow updates in Y")
	}
}

func TestPolicy_OtherOrganizationIsAlwaysDenied(t *testing.T) {
	p := member(authz.Binding{AllGroups: true, Role: role.Admin})
	policy := authz.NewPolicy()

	for _, action := range authz.AllActions() {
		for _, target := range []authz.Target{authz.InOrg(orgB), authz.InGroup(orgB, groupX)} {
			if policy.Allowed(p, action, target) {
				t.Errorf("%s in another organization must be denied", action)
			}
		}
	}
}

func TestPolicy_SuperadminSeesNoTenantDataWithoutMembership(t *testing.T) {
	p := authz.Principal{User: userID, Superadmin: true}
	policy := authz.NewPolicy()

	for _, action := range authz.AllActions() {
		if policy.Allowed(p, action, authz.InOrg(orgA)) || policy.Allowed(p, action, authz.InGroup(orgA, groupX)) {
			t.Errorf("superadmin without membership must not be allowed %s", action)
		}
	}
	for _, action := range []authz.Action{authz.ManageOrganizations, authz.ManageAgentCA, authz.ViewInstanceAudit} {
		if !policy.AllowedInstance(p, action) {
			t.Errorf("superadmin must be allowed %s", action)
		}
		if policy.AllowedInstance(member(authz.Binding{AllGroups: true, Role: role.Admin}), action) {
			t.Errorf("an org admin must not be allowed %s", action)
		}
	}
}

func TestPolicy_ViewersCanBeDeniedLogs(t *testing.T) {
	viewer := member(authz.Binding{AllGroups: true, Role: role.Viewer})
	operator := member(authz.Binding{AllGroups: true, Role: role.Operator})
	policy := authz.NewPolicy(authz.WithViewerLogsDisabled(orgA))
	target := authz.InGroup(orgA, groupX)

	for _, action := range []authz.Action{authz.ViewLogs, authz.ViewSecurityEvents} {
		if policy.Allowed(viewer, action, target) {
			t.Errorf("viewer must not be allowed %s when the org disables logs for viewers", action)
		}
		if !policy.Allowed(operator, action, target) {
			t.Errorf("operator must still be allowed %s", action)
		}
	}
	if !policy.Allowed(viewer, authz.ViewNodes, target) {
		t.Error("the setting only affects logs and security events")
	}
}

func TestPolicy_DeniesInvalidInput(t *testing.T) {
	policy := authz.NewPolicy()
	admin := member(authz.Binding{AllGroups: true, Role: role.Admin})

	cases := map[string]bool{
		"zero principal":              policy.Allowed(authz.Principal{}, authz.ViewNodes, authz.InOrg(orgA)),
		"unknown action":              policy.Allowed(admin, authz.Action(9999), authz.InOrg(orgA)),
		"zero target":                 policy.Allowed(admin, authz.ViewNodes, authz.Target{}),
		"group action, org target":    policy.Allowed(admin, authz.ViewNodes, authz.InOrg(orgA)),
		"invalid role binding":        policy.Allowed(member(authz.Binding{AllGroups: true, Role: role.Role(0)}), authz.ViewNodes, authz.InGroup(orgA, groupX)),
		"unknown instance action":     policy.AllowedInstance(authz.Principal{User: userID, Superadmin: true}, authz.Action(9999)),
		"tenant action at instance":   policy.AllowedInstance(authz.Principal{User: userID, Superadmin: true}, authz.ViewNodes),
		"instance action via Allowed": policy.Allowed(admin, authz.ManageOrganizations, authz.InOrg(orgA)),
	}
	for name, allowed := range cases {
		if allowed {
			t.Errorf("%s: Allowed = true, want false (deny by default)", name)
		}
	}
}

func TestScopeFor_ContainsOnlyGroupsWithTheRequiredRole(t *testing.T) {
	p := member(authz.Binding{Group: groupX, Role: role.Operator}, authz.Binding{Group: groupY, Role: role.Viewer})

	scope, err := authz.ScopeFor(p, role.Operator)
	if err != nil {
		t.Fatalf("ScopeFor() error = %v", err)
	}
	if scope.OrgID() != orgA || !scope.AllowsGroup(groupX) || scope.AllowsGroup(groupY) || scope.AllGroups() {
		t.Errorf("scope = %+v, want org A with only group X", scope)
	}

	all, err := authz.ScopeFor(member(authz.Binding{AllGroups: true, Role: role.Viewer}), role.Viewer)
	if err != nil || !all.AllGroups() {
		t.Errorf("ScopeFor(viewer on *) = %+v, %v; want all groups", all, err)
	}
}

func TestScopeFor_RejectsPrincipalsWithoutOrganization(t *testing.T) {
	if _, err := authz.ScopeFor(authz.Principal{User: userID, Superadmin: true}, role.Viewer); err == nil {
		t.Error("a superadmin without organization must not get a tenant scope")
	}
}

func TestAction_String(t *testing.T) {
	if authz.ViewNodes.String() != "view_nodes" || authz.Action(9999).String() != "unknown" {
		t.Errorf("unexpected action names: %q, %q", authz.ViewNodes, authz.Action(9999))
	}
}
