package updates_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

var (
	org    = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	user   = authz.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	node   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	t0     = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
)

func listed(packages ...update.Package) update.Report {
	return update.Report{CollectedAt: t0, Availability: update.Listed, Manager: update.Apt, Packages: packages}
}

// store has one node in groupX with a report and one without; it records the scope.
type store struct {
	scope tenant.Scope
	fail  bool
}

func (s *store) visible(scope tenant.Scope) bool {
	return scope.AllGroups() || scope.AllowsGroup(groupX)
}

func (s *store) Node(_ context.Context, scope tenant.Scope, id identity.NodeID) (updates.NodeUpdates, bool, error) {
	s.scope = scope
	if s.fail {
		return updates.NodeUpdates{}, false, errors.New("database down")
	}
	if id != node || !s.visible(scope) {
		return updates.NodeUpdates{}, false, nil
	}
	r := listed(update.Package{Name: "openssl", Security: true})
	r.Total, r.Security = 1, 1
	return updates.NodeUpdates{Node: node, Group: groupX, Report: &r}, true, nil
}

func (s *store) Fleet(_ context.Context, scope tenant.Scope) ([]updates.NodeUpdates, error) {
	s.scope = scope
	if s.fail {
		return nil, errors.New("database down")
	}
	if !s.visible(scope) {
		return []updates.NodeUpdates{}, nil
	}
	security := update.Report{Availability: update.Listed, Total: 3, Security: 1, RebootRequired: true}
	current := update.Report{Availability: update.Listed}
	return []updates.NodeUpdates{{Node: node, Report: &security}, {Report: &current}, {Report: &current}, {}}, nil
}

func caller(r role.Role, all bool) access.Caller {
	b := authz.Binding{Group: groupX, Role: r, AllGroups: all}
	return access.Caller{Principal: authz.Principal{User: user, Org: org, Bindings: []authz.Binding{b}}}
}

func TestService_ViewersOfTheGroupSeeTheReport(t *testing.T) {
	s := &store{}
	svc := updates.NewService(s, authz.NewPolicy())
	n, err := svc.Node(context.Background(), caller(role.Viewer, false), node)
	if err != nil || n.Status() != update.StatusSecurity || len(n.Report.Packages) != 1 {
		t.Fatalf("Node() = %+v, %v", n, err)
	}
	if s.scope.AllGroups() || !s.scope.AllowsGroup(groupX) || s.scope.OrgID() != org {
		t.Errorf("scope = %+v, want groupX of the caller's organization", s.scope)
	}
	fleet, err := svc.Fleet(context.Background(), caller(role.Viewer, true))
	if err != nil {
		t.Fatal(err)
	}
	if !s.scope.AllGroups() {
		t.Error("a viewer of all groups gets a narrower scope")
	}
	want := map[update.Status]int{update.StatusSecurity: 1, update.StatusCurrent: 2, update.StatusUnknown: 1}
	if len(fleet.Nodes) != 4 || fleet.RebootRequired != 1 || len(fleet.Counts) != len(want) {
		t.Errorf("fleet = %+v", fleet)
	}
	for status, n := range want {
		if fleet.Counts[status] != n {
			t.Errorf("count of %d = %d, want %d", status, fleet.Counts[status], n)
		}
	}
}

func TestService_OtherNodesAreNotFoundAndNoRightIsForbidden(t *testing.T) {
	svc := updates.NewService(&store{}, authz.NewPolicy())
	ctx := context.Background()
	other := identity.NodeID(uuid.New())
	if _, err := svc.Node(ctx, caller(role.Admin, true), other); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("Node(other) = %v, want not found", err)
	}
	none := access.Caller{Principal: authz.Principal{User: user, Org: org}}
	if _, err := svc.Node(ctx, none, node); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Node() without a role = %v, want forbidden", err)
	}
	if _, err := svc.Fleet(ctx, none); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Fleet() without a role = %v, want forbidden", err)
	}
	failing := updates.NewService(&store{fail: true}, authz.NewPolicy())
	if _, err := failing.Node(ctx, caller(role.Admin, true), node); err == nil || errors.Is(err, access.ErrNotFound) {
		t.Errorf("Node() on a failing store = %v", err)
	}
	if _, err := failing.Fleet(ctx, caller(role.Admin, true)); err == nil {
		t.Error("Fleet() on a failing store succeeded")
	}
}
