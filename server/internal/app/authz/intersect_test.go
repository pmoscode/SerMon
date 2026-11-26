package authz_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Intersect grants on every group and action only what both principals may (a kiosk display
// reads with its creator's and its dashboard owner's rights, ADR 0040 addendum M5-33).
func TestIntersect_GrantsWhatBothMay(t *testing.T) {
	groupZ := tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000cc"))
	other := authz.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	pol := authz.NewPolicy()
	principals := map[string]authz.Principal{
		"admin of all":           {User: userID, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}},
		"operator of all":        {User: other, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Operator}}},
		"viewer of X":            {User: other, Org: orgA, Bindings: []authz.Binding{{Group: groupX, Role: role.Viewer}}},
		"admin of X, viewer *":   {User: other, Org: orgA, Bindings: []authz.Binding{{Group: groupX, Role: role.Admin}, {AllGroups: true, Role: role.Viewer}}},
		"operator of Y":          {User: other, Org: orgA, Bindings: []authz.Binding{{Group: groupY, Role: role.Operator}}},
		"viewer of X, logs off":  {User: other, Org: orgA, ViewersWithoutLogs: true, Bindings: []authz.Binding{{Group: groupX, Role: role.Viewer}}},
		"nothing":                {User: other, Org: orgA},
		"admin of all, logs off": {User: userID, Org: orgA, ViewersWithoutLogs: true, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}},
		"admin of all in B":      {User: other, Org: orgB, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}},
	}
	targets := []authz.Target{authz.InOrg(orgA), authz.InGroup(orgA, groupX), authz.InGroup(orgA, groupY), authz.InGroup(orgA, groupZ)}
	for an, a := range principals {
		for bn, b := range principals {
			both := authz.Intersect(a, b)
			if both.User != a.User || both.Superadmin {
				t.Errorf("%s ∩ %s: user %v, superadmin %v; want the first's user, never a superadmin", an, bn, both.User, both.Superadmin)
			}
			for _, action := range authz.AllActions() {
				for _, target := range targets {
					want := pol.Allowed(a, action, target) && pol.Allowed(b, action, target)
					got := pol.Allowed(both, action, target)
					if got && !want {
						t.Errorf("%s ∩ %s: %s on %+v allowed, but not to both", an, bn, action, target)
					}
					// With an admin of all groups the other's rights stay as they are (the kiosk).
					if strings.HasPrefix(an, "admin of all") && a.Org == b.Org && a.ViewersWithoutLogs == b.ViewersWithoutLogs && got != want {
						t.Errorf("%s ∩ %s: %s on %+v = %v, want %v", an, bn, action, target, got, want)
					}
				}
			}
		}
	}
}

func TestIntersect_NeverGrantsInstanceRights(t *testing.T) {
	super := authz.Principal{User: userID, Org: orgA, Superadmin: true, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}}
	if both := authz.Intersect(super, super); authz.NewPolicy().AllowedInstance(both, authz.ManageOrganizations) {
		t.Error("the intersection must not keep instance rights")
	}
}
