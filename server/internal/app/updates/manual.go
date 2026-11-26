package updates

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Bounds of updates and reboots by hand (M4-14).
const (
	MaxManualNodes = 100
	// ManualWindow is how long a rollout by hand may start nodes.
	ManualWindow = 2 * time.Hour
)

// ErrInvalidRequest means a request by hand is malformed (no nodes, too many, a bad reason).
var ErrInvalidRequest = errors.New("invalid update request")

// ManualStore starts rollouts and reboots by hand, each with its audit entry in one transaction.
type ManualStore interface {
	StartRollouts(ctx context.Context, org tenant.OrgID, rollouts []RolloutRun, now time.Time, audit access.AuditEntry) error
	Enqueue(ctx context.Context, org tenant.OrgID, commands []command.Command, audit access.AuditEntry) error
	// UnderWay reads the pause, the runs under way and which of the nodes are busy (M4-31).
	UnderWay(ctx context.Context, org tenant.OrgID, nodes []identity.NodeID) (UnderWay, error)
	// Rollout returns a rollout of the scope's groups; false if there is none.
	Rollout(ctx context.Context, scope tenant.Scope, id uuid.UUID) (RolloutRun, bool, error)
	// CancelRollout stops a running rollout and cancels its runs not started, with the audit
	// entry; false if it doesn't run any more.
	CancelRollout(ctx context.Context, org tenant.OrgID, id uuid.UUID, by identity.UserID, now time.Time, audit access.AuditEntry) (bool, error)
	// CancelRun cancels a run not started, with the audit entry; false if it started or ended.
	CancelRun(ctx context.Context, org tenant.OrgID, id uuid.UUID, by identity.UserID, now time.Time, audit access.AuditEntry) (bool, error)
}

// WithManual lets the service start updates and reboots by hand.
func (s *Service) WithManual(m ManualStore, policies PolicyStore, newID func() uuid.UUID) *Service {
	s.manual, s.policies, s.newID = m, policies, newID
	return s
}

// visibleNodes resolves the node IDs within what the caller may view (access.ErrNotFound for any
// other) and checks action on each node's group (access.ErrForbidden).
func (s *Service) visibleNodes(ctx context.Context, c access.Caller, ids []identity.NodeID, actions ...authz.Action) ([]NodeUpdates, error) {
	if len(ids) == 0 || len(ids) > MaxManualNodes {
		return nil, fmt.Errorf("%w: 1 to %d nodes", ErrInvalidRequest, MaxManualNodes)
	}
	out := make([]NodeUpdates, 0, len(ids))
	seen := map[identity.NodeID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		n, err := s.Node(ctx, c, id)
		if err != nil {
			return nil, err
		}
		for _, a := range actions {
			if !s.policy.Allowed(c.Principal, a, authz.InGroup(c.Principal.Org, n.Group)) {
				return nil, access.ErrForbidden
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// StartUpdates updates the nodes by hand (run_updates on their groups, reboot_nodes with reboot,
// a fresh step-up): a rollout per group with the group's maxParallel, health check and
// stopOnFailure, run by the orchestrator. It returns the rollouts.
func (s *Service) StartUpdates(ctx context.Context, c access.Caller, ids []identity.NodeID, scope command.Scope, reboot bool) ([]RolloutRun, error) {
	if scope != command.ScopeSecurity && scope != command.ScopeAll {
		return nil, fmt.Errorf("%w: the scope is security or all", ErrInvalidRequest)
	}
	actions := []authz.Action{authz.RunUpdates}
	if reboot {
		actions = append(actions, authz.RebootNodes)
	}
	nodes, err := s.visibleNodes(ctx, c, ids, actions...)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return nil, access.ErrStepUpRequired
	}
	if s.manual == nil {
		return nil, errors.New("no manual store")
	}
	// The rollouts count against the orchestrator's limits; only the pause is checked here.
	if err := s.mayRunByHand(ctx, c.Principal.Org, nodes, len(nodes), false); err != nil {
		return nil, err
	}
	mode := ModeAllAuto
	if scope == command.ScopeSecurity {
		mode = ModeSecurityAuto
	}
	slices.SortFunc(nodes, func(a, b NodeUpdates) int { return strings.Compare(a.Hostname, b.Hostname) })
	byGroup := map[tenant.GroupID]*RolloutRun{}
	var order []tenant.GroupID
	scopeAll, err := tenant.ForAllGroups(c.Principal.Org)
	if err != nil {
		return nil, access.ErrForbidden
	}
	for _, n := range nodes {
		r, ok := byGroup[n.Group]
		if !ok {
			r = &RolloutRun{
				ID: s.newID(), Group: n.Group, WindowStart: now, WindowEnd: now.Add(ManualWindow), State: RolloutRunning,
				Manual: true, Mode: mode, AutoReboot: reboot, CreatedBy: c.Principal.User,
			}
			byGroup[n.Group] = r
			order = append(order, n.Group)
		}
		r.Nodes = append(r.Nodes, NodeRun{Node: n.Node, Hostname: n.Hostname, State: NodeWaiting, Since: now})
	}
	rollouts := make([]RolloutRun, 0, len(order))
	names := make([]string, 0, len(nodes))
	for _, g := range order {
		if _, ok, err := s.policies.Policy(ctx, scopeAll, g); err != nil || !ok {
			return nil, fmt.Errorf("read update policy: %w", errors.Join(err, access.ErrNotFound))
		}
		rollouts = append(rollouts, *byGroup[g])
		for _, n := range byGroup[g].Nodes {
			names = append(names, n.Node.String())
		}
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "updates.start", Target: "nodes:" + strconv.Itoa(len(names)),
		Details: map[string]string{"mode": mode.String(), "reboot": strconv.FormatBool(reboot), "nodes": strings.Join(names, ",")},
	}
	if err := s.manual.StartRollouts(ctx, c.Principal.Org, rollouts, now, audit); err != nil {
		return nil, fmt.Errorf("start updates: %w", err)
	}
	return rollouts, nil
}

// RebootNodes reboots the nodes at once with reason (reboot_nodes on their groups, a fresh
// step-up). The host decides: without allow_remote_reboot its agent reports the refusal.
func (s *Service) RebootNodes(ctx context.Context, c access.Caller, ids []identity.NodeID, reason string) ([]command.Command, error) {
	nodes, err := s.visibleNodes(ctx, c, ids, authz.RebootNodes)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return nil, access.ErrStepUpRequired
	}
	if s.manual == nil {
		return nil, errors.New("no manual store")
	}
	if err := s.mayRunByHand(ctx, c.Principal.Org, nodes, len(nodes), true); err != nil {
		return nil, err
	}
	commands := make([]command.Command, 0, len(nodes))
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		cmd := command.Command{
			ID: s.newID(), Node: n.Node, Kind: command.Reboot, Reason: reason, CreatedAt: now,
			ExpiresAt: now.Add(command.DefaultExpiry), CreatedBy: c.Principal.User,
		}
		if err := cmd.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		commands = append(commands, cmd)
		names = append(names, n.Node.String())
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "nodes.reboot", Target: "nodes:" + strconv.Itoa(len(names)),
		Details: map[string]string{"reason": reason, "nodes": strings.Join(names, ",")},
	}
	if err := s.manual.Enqueue(ctx, c.Principal.Org, commands, audit); err != nil {
		return nil, fmt.Errorf("reboot: %w", err)
	}
	return commands, nil
}
