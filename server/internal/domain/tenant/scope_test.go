package tenant_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	groupY = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
)

func TestForGroups_RestrictsToTheGivenGroups(t *testing.T) {
	scope, err := tenant.ForGroups(orgA, groupX)
	if err != nil {
		t.Fatalf("ForGroups() error = %v", err)
	}

	if scope.OrgID() != orgA {
		t.Errorf("OrgID() = %v, want %v", scope.OrgID(), orgA)
	}
	if !scope.AllowsGroup(groupX) || scope.AllowsGroup(groupY) {
		t.Errorf("AllowsGroup: want only groupX allowed")
	}
	if scope.AllGroups() {
		t.Error("AllGroups() = true, want false")
	}
}

func TestForAllGroups_AllowsEveryGroupOfTheOrg(t *testing.T) {
	scope, err := tenant.ForAllGroups(orgA)
	if err != nil {
		t.Fatalf("ForAllGroups() error = %v", err)
	}

	if !scope.AllGroups() || !scope.AllowsGroup(groupX) || !scope.AllowsGroup(groupY) {
		t.Error("want every group allowed")
	}
}

func TestScope_RejectsMissingOrganization(t *testing.T) {
	_, errAll := tenant.ForAllGroups(tenant.OrgID(uuid.Nil))
	_, errGroups := tenant.ForGroups(tenant.OrgID(uuid.Nil), groupX)

	for _, err := range []error{errAll, errGroups} {
		if !errors.Is(err, tenant.ErrNoOrganization) {
			t.Errorf("error = %v, want ErrNoOrganization", err)
		}
	}
}

func TestScope_ZeroValueIsInvalidAndAllowsNothing(t *testing.T) {
	var scope tenant.Scope

	if scope.Valid() {
		t.Error("zero Scope must not be valid")
	}
	if scope.AllowsGroup(groupX) {
		t.Error("zero Scope must not allow any group")
	}
}

func TestForGroups_WithoutGroupsAllowsNoGroup(t *testing.T) {
	scope, err := tenant.ForGroups(orgA)
	if err != nil {
		t.Fatalf("ForGroups() error = %v", err)
	}

	if !scope.Valid() || scope.AllowsGroup(groupX) {
		t.Error("an org scope without groups is valid but grants no node group")
	}
}

func TestForGroups_CopiesTheGroupList(t *testing.T) {
	groups := []tenant.GroupID{groupX}
	scope, _ := tenant.ForGroups(orgA, groups...)

	groups[0] = groupY // caller mutates its slice afterwards

	if scope.AllowsGroup(groupY) || !scope.AllowsGroup(groupX) {
		t.Error("Scope must not be affected by later changes to the caller's slice")
	}
}

func TestIDs_PrintAsCanonicalUUID(t *testing.T) {
	if got := orgA.String(); got != "0193f5a0-0000-7000-8000-00000000000a" {
		t.Errorf("OrgID.String() = %q", got)
	}
	if got := groupX.String(); got != "0193f5a0-0000-7000-8000-0000000000aa" {
		t.Errorf("GroupID.String() = %q", got)
	}
}

func TestScope_GroupsReturnsACopy(t *testing.T) {
	org := tenant.OrgID(uuid.New())
	g := tenant.GroupID(uuid.New())
	s, _ := tenant.ForGroups(org, g)

	groups := s.Groups()
	groups[0] = tenant.GroupID{}
	if got := s.Groups(); len(got) != 1 || got[0] != g {
		t.Errorf("Groups() = %v, want [%v] (scope must stay immutable)", got, g)
	}
	all, _ := tenant.ForAllGroups(org)
	if len(all.Groups()) != 0 {
		t.Error("ForAllGroups has no explicit group list")
	}
}
