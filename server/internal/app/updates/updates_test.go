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
	"github.com/pmoscode/SerMon/server/internal/domain/command"
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

type refresher struct {
	asked []identity.NodeID
	err   error
}

func (r *refresher) RequestRefresh(_ context.Context, o tenant.OrgID, n identity.NodeID, by identity.UserID, _ time.Time) error {
	if o != org || by != user {
		return errors.New("wrong organization or user")
	}
	r.asked = append(r.asked, n)
	return r.err
}

// M4-11: operators of the node's group ask for a refresh; viewers can't, other nodes are not
// found, the limits come from the refresher.
func TestService_OperatorsAskForFreshUpdateInformation(t *testing.T) {
	r := &refresher{}
	svc := updates.NewService(&store{}, authz.NewPolicy()).WithRefresher(r, func() time.Time { return t0 })
	ctx := context.Background()
	if err := svc.Refresh(ctx, caller(role.Operator, false), node); err != nil || len(r.asked) != 1 || r.asked[0] != node {
		t.Fatalf("Refresh() = %v, asked %v", err, r.asked)
	}
	if err := svc.Refresh(ctx, caller(role.Viewer, true), node); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("a viewer: %v", err)
	}
	if err := svc.Refresh(ctx, caller(role.Admin, true), identity.NodeID(uuid.New())); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("another node: %v", err)
	}
	r.err = updates.ErrTooSoon
	if err := svc.Refresh(ctx, caller(role.Admin, true), node); !errors.Is(err, updates.ErrTooSoon) {
		t.Errorf("too soon: %v", err)
	}
	if len(r.asked) != 2 {
		t.Errorf("asked %d times, want 2 (not for the viewer or another node)", len(r.asked))
	}
}

type manualStore struct {
	rollouts []updates.RolloutRun
	reboots  []command.Command
	audit    []access.AuditEntry
}

func (m *manualStore) StartRollouts(_ context.Context, _ tenant.OrgID, r []updates.RolloutRun, _ time.Time, a access.AuditEntry) error {
	m.rollouts = append(m.rollouts, r...)
	m.audit = append(m.audit, a)
	return nil
}

func (m *manualStore) Reboot(_ context.Context, _ tenant.OrgID, c []command.Command, a access.AuditEntry) error {
	m.reboots = append(m.reboots, c...)
	m.audit = append(m.audit, a)
	return nil
}

func manualService() (*updates.Service, *manualStore) {
	m := &manualStore{}
	svc := updates.NewService(&store{}, authz.NewPolicy()).
		WithRefresher(&refresher{}, func() time.Time { return t0 }).
		WithManual(m, &policyStore{}, uuid.New)
	return svc, m
}

// M4-14: operators of the group update by hand with a fresh step-up; the reboot needs its own
// right; a rollout of the group's policy runs it, the audit log names it.
func TestService_UpdatesByHand(t *testing.T) {
	svc, m := manualService()
	ctx := context.Background()
	operator := fresh(caller(role.Operator, false))
	rollouts, err := svc.StartUpdates(ctx, operator, []identity.NodeID{node, node}, command.ScopeSecurity, true)
	if err != nil {
		t.Fatal(err)
	}
	r := rollouts[0]
	if len(rollouts) != 1 || !r.Manual || r.Mode != updates.ModeSecurityAuto || !r.AutoReboot || r.Group != groupX ||
		len(r.Nodes) != 1 || r.Nodes[0].State != updates.NodeWaiting || !r.WindowEnd.Equal(t0.Add(updates.ManualWindow)) || r.CreatedBy != user {
		t.Errorf("rollout = %+v", r)
	}
	a := m.audit[0]
	if a.Action != "updates.start" || a.Details["mode"] != "security-auto" || a.Details["reboot"] != "true" || a.Details["nodes"] != node.String() {
		t.Errorf("audit = %+v", a)
	}
	for name, c := range map[string]access.Caller{
		"viewer": fresh(caller(role.Viewer, true)), "no step-up": caller(role.Operator, true),
	} {
		if _, err := svc.StartUpdates(ctx, c, []identity.NodeID{node}, command.ScopeAll, false); !errors.Is(err, access.ErrForbidden) && !errors.Is(err, access.ErrStepUpRequired) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.StartUpdates(ctx, operator, []identity.NodeID{identity.NodeID(uuid.New())}, command.ScopeAll, false); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("another node: %v", err)
	}
	for name, ids := range map[string][]identity.NodeID{"none": nil, "too many": make([]identity.NodeID, updates.MaxManualNodes+1)} {
		if _, err := svc.StartUpdates(ctx, operator, ids, command.ScopeAll, false); !errors.Is(err, updates.ErrInvalidRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.StartUpdates(ctx, operator, []identity.NodeID{node}, 0, false); !errors.Is(err, updates.ErrInvalidRequest) {
		t.Errorf("no scope: %v", err)
	}
	if len(m.rollouts) != 1 {
		t.Errorf("%d rollouts, want only the first", len(m.rollouts))
	}
}

func TestService_RebootsByHand(t *testing.T) {
	svc, m := manualService()
	ctx := context.Background()
	reboots, err := svc.RebootNodes(ctx, fresh(caller(role.Operator, false)), []identity.NodeID{node}, "kernel update")
	if err != nil || len(reboots) != 1 || reboots[0].Kind != command.Reboot || reboots[0].Reason != "kernel update" || reboots[0].CreatedBy != user {
		t.Fatalf("RebootNodes() = %+v, %v", reboots, err)
	}
	if a := m.audit[0]; a.Action != "nodes.reboot" || a.Details["reason"] != "kernel update" {
		t.Errorf("audit = %+v", a)
	}
	if _, err := svc.RebootNodes(ctx, fresh(caller(role.Viewer, true)), []identity.NodeID{node}, "x"); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer: %v", err)
	}
	if _, err := svc.RebootNodes(ctx, caller(role.Admin, true), []identity.NodeID{node}, "x"); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("no step-up: %v", err)
	}
	if _, err := svc.RebootNodes(ctx, fresh(caller(role.Admin, true)), []identity.NodeID{node}, "now\n-h"); !errors.Is(err, updates.ErrInvalidRequest) {
		t.Errorf("control characters: %v", err)
	}
	if len(m.reboots) != 1 {
		t.Errorf("%d reboots", len(m.reboots))
	}
}
