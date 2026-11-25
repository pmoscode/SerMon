package updates

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// NodeState is where a node of a rollout stands (KONZEPT §14.2, M4-13).
type NodeState uint8

// The states of a node in a rollout.
const (
	NodeWaiting NodeState = iota + 1
	NodeUpdating
	NodeRebooting
	NodeHealthCheck
	NodeDone
	NodeFailed
	NodeSkipped
)

// Active reports whether the node takes one of the rollout's parallel places.
func (s NodeState) Active() bool {
	return s == NodeUpdating || s == NodeRebooting || s == NodeHealthCheck
}

// RolloutState is where a rollout stands.
type RolloutState uint8

// The states of a rollout.
const (
	RolloutRunning RolloutState = iota + 1
	RolloutDone
	RolloutStopped
)

// Timing of a rollout.
const (
	// Settle is how long a node must stay healthy after its update (or its new session after a
	// reboot) before it counts as done: the alert engine needs a round to see it.
	Settle = time.Minute
	// ReportWait is how long the rollout waits for the node's report after its update before it
	// decides about the reboot with what it knows.
	ReportWait = 5 * time.Minute
	// RebootReason is shown on the host and in its journal.
	RebootReason = "updates (SerMon)"
)

// NodeRun is a node in a rollout.
type NodeRun struct {
	Node     identity.NodeID
	Hostname string
	State    NodeState
	// Reason says why a node failed or was skipped.
	Reason    string
	UpdateRun uuid.UUID
	RebootRun uuid.UUID
	// Since is when the node entered its state; Deadline ends its health check.
	Since    time.Time
	Deadline time.Time
}

// RolloutRun is the run of a group's policy in one window.
type RolloutRun struct {
	ID          uuid.UUID
	Group       tenant.GroupID
	WindowStart time.Time
	WindowEnd   time.Time
	State       RolloutState
	Reason      string
	Nodes       []NodeRun
}

// NodeView is what the rollout knows of a node of the group now.
type NodeView struct {
	ID       identity.NodeID
	Hostname string
	Online   bool
	// ConnectedAt is when its current (or last) session began.
	ConnectedAt time.Time
	// Report is its latest update report (nil: none); ReportAt when the server stored it.
	Report   *update.Report
	ReportAt time.Time
	// Critical: an alert of the node is critical now (the maintenance rules excluded).
	Critical bool
}

// Action is what a planning step asks the store to do with the rollout's new state.
type Action struct {
	// Command is enqueued for its node.
	Command *command.Command
	// SilenceUntil mutes the node's reboot and offline alerts until then (with Command).
	SilenceUntil time.Time
	// Event is a server event of the node: EventRunFailed or EventRolloutStopped.
	Event  string
	Node   identity.NodeID
	Reason string
}

// The server events of a rollout (M4-16 turns them into rules).
const (
	EventRunFailed      = "updates.run_failed"
	EventRolloutStopped = "updates.rollout_stopped"
)

// Room is how many runs the rollout may start now: the organization's and the instance's limits
// (KONZEPT §14.2: limits per organization and instance).
type Room int

// Plan moves the group's rollout one step at now: it starts the rollout when a window of the
// policy begins, starts waiting nodes while the window is open and places are free (at most
// MaxParallel and room), follows the runs of the active nodes and ends the rollout. current is
// the group's last rollout (nil: none); runs are the commands of its active nodes; newID makes
// the IDs of runs and rollouts. It returns the rollout to store (nil: nothing to store) and the
// actions.
func Plan(now time.Time, p Policy, current *RolloutRun, nodes []NodeView, runs map[uuid.UUID]command.Command, room Room, newID func() uuid.UUID) (*RolloutRun, []Action) {
	r := current
	// A stopped rollout still follows its nodes in a run; no new node starts.
	if r == nil || !r.Following() {
		r = begin(now, p, current, nodes, newID)
		if r == nil {
			return nil, nil
		}
	} else {
		copied := *r
		copied.Nodes = slices.Clone(r.Nodes)
		r = &copied
	}
	views := map[identity.NodeID]NodeView{}
	for _, n := range nodes {
		views[n.ID] = n
	}
	var actions []Action
	windowOpen := now.Before(r.WindowEnd) && p.Mode != ModeReport
	for i := range r.Nodes {
		n := &r.Nodes[i]
		if !n.State.Active() {
			continue
		}
		if failed := follow(now, p, n, views[n.Node], runs, windowOpen, newID, &actions); failed && p.Rollout.StopOnFailure && r.State == RolloutRunning {
			r.State, r.Reason = RolloutStopped, n.Hostname+": "+n.Reason
			actions = append(actions, Action{Event: EventRolloutStopped, Node: n.Node, Reason: r.Reason})
		}
	}
	active := 0
	for _, n := range r.Nodes {
		if n.State.Active() {
			active++
		}
	}
	for i := range r.Nodes {
		n := &r.Nodes[i]
		if n.State != NodeWaiting {
			continue
		}
		if r.State != RolloutRunning || !windowOpen {
			n.State, n.Reason, n.Since = NodeSkipped, "the window ended", now
			if r.State == RolloutStopped {
				n.Reason = "the rollout stopped"
			}
			continue
		}
		if active >= p.Rollout.MaxParallel || room <= 0 {
			break
		}
		v, known := views[n.Node]
		mode := ModeReport
		if known && v.Report != nil {
			mode, _ = p.ModeFor(v.Report.Manager)
		}
		switch {
		case !known:
			n.State, n.Reason = NodeSkipped, "the node is gone"
		case !v.Online:
			n.State, n.Reason = NodeSkipped, "the node is offline"
		case v.Report == nil || v.Report.Permissions == nil:
			n.State, n.Reason = NodeSkipped, "the node has no root helper"
		case !v.Report.Permissions.RemoteUpdates:
			n.State, n.Reason = NodeSkipped, "the host forbids remote updates (allow_remote_updates)"
		case mode == ModeReport:
			n.State, n.Reason = NodeSkipped, "Alpine has no security metadata: security-auto only reports"
		case !wants(mode, v.Report):
			n.State, n.Reason = NodeDone, "nothing to update any more"
		default:
			scope := command.ScopeAll
			if mode == ModeSecurityAuto {
				scope = command.ScopeSecurity
			}
			c := command.Command{
				ID: newID(), Node: n.Node, Kind: command.Update, Scope: scope, CreatedAt: now, ExpiresAt: now.Add(command.DefaultExpiry),
			}
			n.State, n.UpdateRun = NodeUpdating, c.ID
			active++
			room--
			actions = append(actions, Action{Command: &c, Node: n.Node, SilenceUntil: r.WindowEnd.Add(p.Rollout.HealthCheck)})
		}
		n.Since = now
	}
	if r.State == RolloutRunning && !slices.ContainsFunc(r.Nodes, func(n NodeRun) bool { return n.State == NodeWaiting || n.State.Active() }) {
		r.State = RolloutDone
	}
	return r, actions
}

// stillFollowing reports whether a stopped rollout has nodes to follow.
func (r RolloutRun) stillFollowing() bool {
	return slices.ContainsFunc(r.Nodes, func(n NodeRun) bool { return n.State.Active() })
}

// Following reports whether the planner still has to look at the rollout: running, or stopped
// with nodes in a run.
func (r RolloutRun) Following() bool { return r.State == RolloutRunning || r.stillFollowing() }

// wants reports whether the node has what the mode installs.
func wants(mode Mode, r *update.Report) bool {
	if r.Availability != update.Listed {
		return false
	}
	if mode == ModeSecurityAuto {
		return r.Security > 0
	}
	return r.Total > 0
}

// begin starts a rollout when a window of the policy is open and none ran in it yet; the nodes
// with something to install, by host name.
func begin(now time.Time, p Policy, last *RolloutRun, nodes []NodeView, newID func() uuid.UUID) *RolloutRun {
	if p.Mode == ModeReport || p.Window == nil {
		return nil
	}
	start, end, ok := p.Window.Current(now)
	if !ok || (last != nil && !last.WindowStart.Before(start)) {
		return nil
	}
	r := &RolloutRun{ID: newID(), Group: p.Group, WindowStart: start, WindowEnd: end, State: RolloutRunning}
	sorted := slices.SortedFunc(slices.Values(nodes), func(a, b NodeView) int {
		return cmp.Or(cmp.Compare(a.Hostname, b.Hostname), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	for _, n := range sorted {
		if n.Report == nil {
			continue
		}
		mode, _ := p.ModeFor(n.Report.Manager)
		if mode != ModeReport && !wants(mode, n.Report) {
			continue
		}
		if mode == ModeReport && !wants(ModeAllAuto, n.Report) {
			continue
		}
		r.Nodes = append(r.Nodes, NodeRun{Node: n.ID, Hostname: n.Hostname, State: NodeWaiting, Since: now})
	}
	return r
}

// follow moves an active node on; true if it failed now.
func follow(now time.Time, p Policy, n *NodeRun, v NodeView, runs map[uuid.UUID]command.Command, windowOpen bool, newID func() uuid.UUID, actions *[]Action) bool {
	fail := func(reason string) bool {
		n.State, n.Reason, n.Since = NodeFailed, reason, now
		*actions = append(*actions, Action{Event: EventRunFailed, Node: n.Node, Reason: reason})
		return true
	}
	switch n.State {
	case NodeUpdating:
		run, ok := runs[n.UpdateRun]
		if done, reason := finished(run, ok, now); !done {
			return false
		} else if reason != "" {
			return fail("update: " + reason)
		}
		ended := endOf(run)
		// The reboot is decided with the report after the update, or what is known after a while.
		if v.ReportAt.Before(ended) && now.Before(ended.Add(ReportWait)) {
			return false
		}
		needs := v.Report != nil && v.Report.RebootRequired
		allowed := v.Report != nil && v.Report.Permissions != nil && v.Report.Permissions.RemoteReboot
		if needs && p.AutoReboot && windowOpen && allowed {
			c := command.Command{
				ID: newID(), Node: n.Node, Kind: command.Reboot, Reason: RebootReason, CreatedAt: now, ExpiresAt: now.Add(command.DefaultExpiry),
			}
			n.State, n.RebootRun, n.Since = NodeRebooting, c.ID, now
			*actions = append(*actions, Action{Command: &c, Node: n.Node})
			return false
		}
		n.State, n.Since, n.Deadline = NodeHealthCheck, now, now.Add(p.Rollout.HealthCheck)
	case NodeRebooting:
		run, ok := runs[n.RebootRun]
		if done, reason := finished(run, ok, now); !done {
			return false
		} else if reason != "" {
			return fail("reboot: " + reason)
		}
		n.State, n.Since, n.Deadline = NodeHealthCheck, endOf(run), endOf(run).Add(p.Rollout.HealthCheck)
	case NodeHealthCheck:
		back := v.Online && !v.ConnectedAt.Before(n.Since)
		if n.RebootRun == uuid.Nil {
			back = v.Online
		}
		if back && !v.Critical && !now.Before(n.Since.Add(Settle)) {
			n.State, n.Since = NodeDone, now
			return false
		}
		if !now.Before(n.Deadline) {
			reason := "not back online in time"
			if v.Online && v.Critical {
				reason = "a critical alert is firing"
			}
			return fail("health check: " + reason)
		}
	case NodeWaiting, NodeDone, NodeFailed, NodeSkipped:
	}
	return false
}

// finished reports whether a run ended, and why it failed if it did not succeed.
func finished(run command.Command, known bool, now time.Time) (bool, string) {
	if !known {
		return true, "the run is gone"
	}
	if run.EffectiveState(now) == command.Expired {
		return true, "the node didn't take the command in time"
	}
	if run.Result == nil || !run.Result.Phase.Final() {
		return false, ""
	}
	switch run.Result.Phase {
	case command.Succeeded:
		return true, ""
	case command.Refused, command.Unsupported:
		return true, "not run: " + run.Result.Reason
	case command.Lost:
		return true, "the root helper lost the run"
	case command.PhaseUnknown, command.Started, command.Failed:
	}
	if run.Result.Reason != "" {
		return true, "failed: " + run.Result.Reason
	}
	return true, fmt.Sprintf("failed with exit code %d", run.Result.ExitCode)
}

func endOf(run command.Command) time.Time {
	if run.Result != nil && run.Result.EndedAt != nil {
		return *run.Result.EndedAt
	}
	if run.SentAt != nil {
		return *run.SentAt
	}
	return run.CreatedAt
}
