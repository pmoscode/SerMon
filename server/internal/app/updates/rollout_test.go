package updates_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// A Sunday window 03:00–05:00 in Berlin; 22 November 2026 is a Sunday (winter time, UTC+1).
var (
	berlin, _   = time.LoadLocation("Europe/Berlin")
	windowStart = time.Date(2026, 11, 22, 3, 0, 0, 0, berlin)
)

func policy(mode updates.Mode, parallel int, stop, reboot bool) updates.Policy {
	return updates.Policy{
		Group: groupX, Mode: mode, AutoReboot: reboot,
		Window:  &routing.Schedule{Days: []time.Weekday{time.Sunday}, Start: 3 * time.Hour, Duration: 2 * time.Hour, TimeZone: "Europe/Berlin"},
		Rollout: updates.Rollout{MaxParallel: parallel, StopOnFailure: stop, HealthCheck: 10 * time.Minute},
	}
}

func nodeID(i int) identity.NodeID {
	return identity.NodeID(uuid.MustParse(fmt.Sprintf("0193f5a0-0000-7000-8000-%012d", i)))
}

// world is a group of nodes with their reports, the commands and a clock.
type world struct {
	t       *testing.T
	now     time.Time
	policy  updates.Policy
	nodes   []updates.NodeView
	runs    map[uuid.UUID]command.Command
	rollout *updates.RolloutRun
	actions []updates.Action
	room    updates.Room
	ids     int
}

func newWorld(t *testing.T, p updates.Policy, n int) *world {
	t.Helper()
	w := &world{t: t, now: windowStart.Add(-time.Minute), policy: p, runs: map[uuid.UUID]command.Command{}, room: 100}
	for i := range n {
		w.nodes = append(w.nodes, updates.NodeView{
			ID: nodeID(i), Hostname: fmt.Sprintf("web-%02d", n-i), Online: true, ConnectedAt: w.now.Add(-time.Hour),
			Report: &update.Report{
				Availability: update.Listed, Manager: update.Apt, Total: 3, Security: 1,
				Permissions: &update.Permissions{RemoteUpdates: true, RemoteReboot: true},
			},
			ReportAt: w.now.Add(-time.Hour),
		})
	}
	return w
}

func (w *world) view(host string) *updates.NodeView {
	for i := range w.nodes {
		if w.nodes[i].Hostname == host {
			return &w.nodes[i]
		}
	}
	w.t.Fatalf("no node %s", host)
	return nil
}

// step plans at the current time; the commands go into runs (sent, without result).
func (w *world) step() []updates.Action {
	w.t.Helper()
	r, actions := updates.Plan(w.now, w.policy, w.rollout, w.nodes, w.runs, w.room, func() uuid.UUID {
		w.ids++
		return uuid.MustParse(fmt.Sprintf("0193f5a0-0000-7000-9000-%012d", w.ids))
	})
	if r != nil {
		w.rollout = r
	}
	for _, a := range actions {
		if a.Command != nil {
			c := *a.Command
			c.State = command.Sent
			w.runs[c.ID] = c
		}
	}
	w.actions = append(w.actions, actions...)
	return actions
}

func (w *world) at(d time.Duration) *world {
	w.now = windowStart.Add(d)
	return w
}

// finish ends the node's current run with phase (and, for an update, a new report).
func (w *world) finish(host string, phase command.Phase, rebootNeeded bool) {
	w.t.Helper()
	n := w.run(host)
	id := n.UpdateRun
	if n.State == updates.NodeRebooting {
		id = n.RebootRun
	}
	if n.State == updates.NodeAgentUpdating {
		id = n.AgentRun
	}
	c := w.runs[id]
	end := w.now
	c.Result, c.State = &command.Result{Phase: phase, EndedAt: &end, ExitCode: 100}, command.Done
	w.runs[id] = c
	v := w.view(host)
	if c.Kind == command.Update && phase == command.Succeeded {
		report := *v.Report
		report.Total, report.Security, report.RebootRequired = 0, 0, rebootNeeded
		v.Report, v.ReportAt = &report, w.now
	}
}

func (w *world) run(host string) updates.NodeRun {
	for _, n := range w.rollout.Nodes {
		if n.Hostname == host {
			return n
		}
	}
	w.t.Fatalf("%s not in the rollout", host)
	return updates.NodeRun{}
}

func (w *world) states() string {
	var out []string
	for _, n := range w.rollout.Nodes {
		out = append(out, fmt.Sprintf("%s=%d", n.Hostname, n.State))
	}
	return strings.Join(out, " ")
}

func commands(actions []updates.Action) []command.Kind {
	var out []command.Kind
	for _, a := range actions {
		if a.Command != nil {
			out = append(out, a.Command.Kind)
		}
	}
	return out
}

// Nothing happens before the window; at its start the nodes with security updates line up by host
// name and at most maxParallel start, with their reboot and offline alerts muted.
func TestPlan_StartsAtTheWindowWithMaxParallelInOrder(t *testing.T) {
	w := newWorld(t, policy(updates.ModeSecurityAuto, 2, true, true), 4)
	w.view("web-03").Report.Security = 0 // only ordinary updates: not in security-auto
	if r, a := updates.Plan(w.now, w.policy, nil, w.nodes, w.runs, w.room, uuid.New); r != nil || a != nil {
		t.Fatalf("before the window: %+v %+v", r, a)
	}
	actions := w.at(0).step()
	if got := w.states(); got != "web-01=2 web-02=2 web-04=1" {
		t.Fatalf("states = %s", got)
	}
	if !w.rollout.WindowStart.Equal(windowStart) || !w.rollout.WindowEnd.Equal(windowStart.Add(2*time.Hour)) {
		t.Errorf("window = %v – %v", w.rollout.WindowStart, w.rollout.WindowEnd)
	}
	for _, a := range actions {
		if a.Command == nil || a.Command.Kind != command.Update || a.Command.Scope != command.ScopeSecurity ||
			!a.SilenceUntil.Equal(windowStart.Add(2*time.Hour+10*time.Minute)) {
			t.Errorf("action = %+v", a)
		}
	}
	// The next step starts nobody while both places are taken.
	if a := w.at(time.Minute).step(); len(a) != 0 {
		t.Errorf("started while full: %+v", a)
	}
}

// A node through update, reboot and health check; the next one starts when a place frees up.
func TestPlan_UpdateRebootAndHealthCheck(t *testing.T) {
	w := newWorld(t, policy(updates.ModeAllAuto, 1, true, true), 2)
	w.at(0).step()
	w.at(3*time.Minute).finish("web-01", command.Succeeded, true)
	if a := w.at(4 * time.Minute).step(); len(a) != 1 || a[0].Command.Kind != command.Reboot || a[0].Command.Reason != updates.RebootReason {
		t.Fatalf("after the update: %+v (%s)", a, w.states())
	}
	w.at(5*time.Minute).finish("web-01", command.Succeeded, false)
	w.at(6 * time.Minute).step()
	if n := w.run("web-01"); n.State != updates.NodeHealthCheck {
		t.Fatalf("after the reboot: %s", w.states())
	}
	// Online with the old session: not back yet. A new session after the reboot, a minute settled.
	w.at(7 * time.Minute).step()
	if w.run("web-01").State != updates.NodeHealthCheck {
		t.Fatal("the old session counted")
	}
	w.view("web-01").ConnectedAt = windowStart.Add(7 * time.Minute)
	w.at(8 * time.Minute).step()
	if w.run("web-01").State != updates.NodeDone || w.run("web-02").State != updates.NodeUpdating {
		t.Fatalf("after the health check: %s", w.states())
	}
	w.at(10*time.Minute).finish("web-02", command.Succeeded, false)
	w.at(11 * time.Minute).step()
	w.at(12 * time.Minute).step()
	if w.rollout.State != updates.RolloutDone || w.run("web-02").State != updates.NodeDone || w.run("web-02").RebootRun != uuid.Nil {
		t.Errorf("end: %s, state %d", w.states(), w.rollout.State)
	}
	// Done for this window: no second rollout in it, the next Sunday a new one.
	if r, _ := updates.Plan(windowStart.Add(30*time.Minute), w.policy, w.rollout, w.nodes, w.runs, 10, uuid.New); r != nil {
		t.Errorf("a second rollout in the same window: %+v", r)
	}
	w.view("web-01").Report.Total = 1
	if r, _ := updates.Plan(windowStart.AddDate(0, 0, 7), w.policy, w.rollout, w.nodes, w.runs, 10, uuid.New); r == nil || len(r.Nodes) != 1 {
		t.Errorf("next Sunday: %+v", r)
	}
}

// Rebooting needs autoReboot, a reboot the host needs and allows, and an open window.
func TestPlan_RebootsOnlyWhenNeededAllowedAndInTheWindow(t *testing.T) {
	for name, c := range map[string]struct {
		autoReboot, needed, allowed bool
		finishAt                    time.Duration
		reboot                      bool
	}{
		"all given":     {true, true, true, time.Minute, true},
		"no autoReboot": {false, true, true, time.Minute, false},
		"not needed":    {true, false, true, time.Minute, false},
		"host forbids":  {true, true, false, time.Minute, false},
		"window ended":  {true, true, true, 2 * time.Hour, false},
	} {
		w := newWorld(t, policy(updates.ModeAllAuto, 1, false, c.autoReboot), 1)
		w.view("web-01").Report.Permissions.RemoteReboot = c.allowed
		w.at(0).step()
		w.at(c.finishAt).finish("web-01", command.Succeeded, c.needed)
		got := commands(w.at(c.finishAt + time.Second).step())
		if (len(got) == 1 && got[0] == command.Reboot) != c.reboot {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// The reboot waits for the report after the update, at most ReportWait.
func TestPlan_WaitsForTheReportBeforeTheReboot(t *testing.T) {
	w := newWorld(t, policy(updates.ModeAllAuto, 1, false, true), 1)
	w.at(0).step()
	w.view("web-01").Report.RebootRequired = true
	c := w.runs[w.run("web-01").UpdateRun]
	end := windowStart.Add(time.Minute)
	c.Result = &command.Result{Phase: command.Succeeded, EndedAt: &end}
	w.runs[c.ID] = c
	if a := w.at(3 * time.Minute).step(); len(a) != 0 {
		t.Fatalf("decided without the new report: %+v", a)
	}
	if got := commands(w.at(time.Minute + updates.ReportWait).step()); len(got) != 1 || got[0] != command.Reboot {
		t.Errorf("after ReportWait with the old report: %v", got)
	}
}

// A failed update with stopOnFailure stops the rollout: events, waiting nodes skipped, a node
// still running finishes. Without it the rollout goes on.
func TestPlan_AFailureStopsTheRolloutOnlyWithStopOnFailure(t *testing.T) {
	w := newWorld(t, policy(updates.ModeAllAuto, 2, true, false), 4)
	w.at(0).step()
	w.at(time.Minute).finish("web-01", command.Failed, false)
	actions := w.at(2 * time.Minute).step()
	if w.rollout.State != updates.RolloutStopped || !strings.Contains(w.rollout.Reason, "web-01: update: failed with exit code 100") {
		t.Fatalf("rollout = %d %q", w.rollout.State, w.rollout.Reason)
	}
	events := map[string]bool{}
	for _, a := range actions {
		events[a.Event] = true
	}
	if !events[updates.EventRunFailed] || !events[updates.EventRolloutStopped] || len(commands(actions)) != 0 {
		t.Errorf("actions = %+v", actions)
	}
	if got := w.states(); got != "web-01=6 web-02=2 web-03=7 web-04=7" {
		t.Errorf("states = %s", got)
	}
	// web-02 still finishes.
	w.at(3*time.Minute).finish("web-02", command.Succeeded, false)
	w.at(4 * time.Minute).step()
	w.at(6 * time.Minute).step()
	if w.run("web-02").State != updates.NodeDone || w.rollout.State != updates.RolloutStopped {
		t.Errorf("after the stop: %s %d", w.states(), w.rollout.State)
	}

	w = newWorld(t, policy(updates.ModeAllAuto, 1, false, false), 2)
	w.at(0).step()
	w.at(time.Minute).finish("web-01", command.Refused, false)
	if got := commands(w.at(2 * time.Minute).step()); len(got) != 1 || w.rollout.State != updates.RolloutRunning {
		t.Errorf("without stopOnFailure: %v, %d", got, w.rollout.State)
	}
}

// A node not back in time, or back with a critical alert, fails its health check.
func TestPlan_AMissedHealthCheckFails(t *testing.T) {
	for name, critical := range map[string]bool{"offline": false, "critical": true} {
		w := newWorld(t, policy(updates.ModeAllAuto, 1, true, true), 1)
		w.at(0).step()
		w.at(time.Minute).finish("web-01", command.Succeeded, true)
		w.at(2 * time.Minute).step()
		w.at(3*time.Minute).finish("web-01", command.Succeeded, false)
		w.view("web-01").Online = critical
		w.view("web-01").Critical = critical
		w.view("web-01").ConnectedAt = windowStart.Add(4 * time.Minute)
		// The deadline counts from when the server saw the reboot end (4 min), not the host's time.
		w.at(4 * time.Minute).step()
		w.at(13 * time.Minute).step()
		if w.run("web-01").State != updates.NodeHealthCheck {
			t.Fatalf("%s: failed before the deadline: %s", name, w.states())
		}
		w.at(14 * time.Minute).step()
		n := w.run("web-01")
		want := map[bool]string{false: "not back online in time", true: "a critical alert is firing"}[critical]
		if n.State != updates.NodeFailed || !strings.Contains(n.Reason, want) || w.rollout.State != updates.RolloutStopped {
			t.Errorf("%s: %+v, rollout %d", name, n, w.rollout.State)
		}
	}
}

// Offline nodes, nodes without helper or with the host's veto and Alpine in security-auto are
// skipped with their reason; a node with nothing left is done.
func TestPlan_SkipsWhatCannotUpdate(t *testing.T) {
	w := newWorld(t, policy(updates.ModeSecurityAuto, 10, false, false), 5)
	w.view("web-05").Online = false
	w.view("web-04").Report.Permissions = nil
	w.view("web-03").Report.Permissions.RemoteUpdates = false
	w.view("web-02").Report.Manager = update.Apk
	w.view("web-02").Report.Security = 0
	w.at(0).step()
	for host, reason := range map[string]string{
		"web-05": "offline", "web-04": "no root helper", "web-03": "allow_remote_updates", "web-02": "Alpine",
	} {
		if n := w.run(host); n.State != updates.NodeSkipped || !strings.Contains(n.Reason, reason) {
			t.Errorf("%s: %+v", host, n)
		}
	}
	if n := w.run("web-01"); n.State != updates.NodeUpdating {
		t.Errorf("web-01: %+v", n)
	}
}

// At the window's end no node starts; the running one finishes after it.
func TestPlan_NoNewNodeAfterTheWindow(t *testing.T) {
	w := newWorld(t, policy(updates.ModeAllAuto, 1, false, false), 2)
	w.at(0).step()
	w.at(2*time.Hour).finish("web-01", command.Succeeded, false)
	w.at(2*time.Hour + time.Minute).step()
	if got := w.states(); got != "web-01=4 web-02=7" || w.run("web-02").Reason != "the window ended" {
		t.Fatalf("at the end: %s %q", got, w.run("web-02").Reason)
	}
	w.at(2*time.Hour + 3*time.Minute).step()
	if w.run("web-01").State != updates.NodeDone || w.rollout.State != updates.RolloutDone {
		t.Errorf("after the end: %s %d", w.states(), w.rollout.State)
	}
}

// The organization's and the instance's room bound what starts.
func TestPlan_RoomBoundsTheStarts(t *testing.T) {
	w := newWorld(t, policy(updates.ModeAllAuto, 10, false, false), 5)
	w.room = 2
	if got := commands(w.at(0).step()); len(got) != 2 {
		t.Errorf("started %d with room for 2", len(got))
	}
	w.room = 0
	if got := commands(w.at(time.Minute).step()); len(got) != 0 {
		t.Errorf("started %d without room", len(got))
	}
}

// A command the node never takes expires: the update failed.
func TestPlan_AnExpiredCommandFails(t *testing.T) {
	w := newWorld(t, policy(updates.ModeAllAuto, 1, false, false), 1)
	w.at(0).step()
	w.at(command.DefaultExpiry).step()
	if n := w.run("web-01"); n.State != updates.NodeFailed || !strings.Contains(n.Reason, "in time") {
		t.Errorf("%+v", n)
	}
}

// Report only, no window, nothing to do: no rollout.
func TestPlan_ReportOnlyDoesNothing(t *testing.T) {
	w := newWorld(t, updates.DefaultPolicy(groupX), 2)
	if r, a := updates.Plan(windowStart, w.policy, nil, w.nodes, w.runs, 10, uuid.New); r != nil || a != nil {
		t.Errorf("%+v %+v", r, a)
	}
	w = newWorld(t, policy(updates.ModeAllAuto, 1, false, false), 1)
	w.view("web-01").Report.Total = 0
	w.at(0).step()
	if w.rollout == nil || w.rollout.State != updates.RolloutDone || len(w.rollout.Nodes) != 0 {
		t.Errorf("nothing to update: %+v", w.rollout)
	}
}

// Without a reboot the node must stay healthy for Settle after its update before it is done.
func TestPlan_AHealthyNodeSettlesBeforeItIsDone(t *testing.T) {
	w := newWorld(t, policy(updates.ModeAllAuto, 1, true, false), 1)
	w.at(0).step()
	w.at(time.Minute).finish("web-01", command.Succeeded, false)
	w.at(time.Minute + time.Second).step()
	w.at(time.Minute + 30*time.Second).step()
	if n := w.run("web-01"); n.State != updates.NodeHealthCheck {
		t.Fatalf("done before settling: %+v", n)
	}
	w.at(2*time.Minute + time.Second).step()
	if n := w.run("web-01"); n.State != updates.NodeDone {
		t.Errorf("after settling: %+v", n)
	}
}

func agentPolicy(mode updates.Mode) updates.Policy {
	p := policy(mode, 1, true, false)
	p.AgentUpdate, p.Channel = updates.AgentAuto, update.ChannelStable
	return p
}

// Review M4-30: the host's times don't count. A new agent connects before its helper notes the
// end (the order of the real self-update); a host whose clock runs ahead or reports an end far
// in the future doesn't hold the health check.
func TestPlan_TheHealthCheckRunsOnTheServersTimes(t *testing.T) {
	w := newWorld(t, agentPolicy(updates.ModeReport), 1)
	w.view("web-01").AgentTarget = "1.1.0"
	w.view("web-01").Report.Permissions.SelfUpdate = true
	w.at(0).step()
	// The new agent connects at 1 min, its helper ends at 2 min (host clock: in a year).
	w.view("web-01").ConnectedAt = windowStart.Add(time.Minute)
	w.at(2*time.Minute).finish("web-01", command.Succeeded, false)
	run := w.run("web-01")
	c := w.runs[run.AgentRun]
	future := windowStart.Add(365 * 24 * time.Hour)
	c.Result.EndedAt = &future
	w.runs[run.AgentRun] = c
	w.at(2*time.Minute + time.Second).step()
	if n := w.run("web-01"); n.State != updates.NodeHealthCheck || !n.Since.Equal(windowStart.Add(2*time.Minute+time.Second)) || !n.Deadline.Before(future) {
		t.Fatalf("health check from the host's time: %+v", n)
	}
	w.at(4 * time.Minute).step()
	if n := w.run("web-01"); n.State != updates.NodeDone {
		t.Errorf("the new session before the helper's end didn't count: %+v", n)
	}
}

// Review M4-30: after a reboot the node is back with a session begun after the server sent the
// reboot – also when the agent reports the reboot's end only after it connected again (as it
// does: the result outlives the reboot). A session from before the reboot doesn't count.
func TestPlan_BackAfterARebootCountsFromItsSending(t *testing.T) {
	for name, connected := range map[string]time.Duration{"after the reboot": 150 * time.Second, "before it": 90 * time.Second} {
		w := newWorld(t, policy(updates.ModeAllAuto, 1, true, true), 1)
		w.at(0).step()
		w.at(time.Minute).finish("web-01", command.Succeeded, true)
		w.at(2 * time.Minute).step() // the reboot, sent at 2 min
		w.view("web-01").ConnectedAt = windowStart.Add(connected)
		w.at(3*time.Minute).finish("web-01", command.Succeeded, false)
		w.at(4 * time.Minute).step() // health check from 4 min (server time)
		w.at(6 * time.Minute).step()
		n := w.run("web-01")
		if want := map[bool]updates.NodeState{true: updates.NodeDone, false: updates.NodeHealthCheck}[connected > 2*time.Minute]; n.State != want {
			t.Errorf("%s: %+v, want state %d", name, n, want)
		}
	}
}

// M4-19: with agentUpdate auto a node with a newer release updates its agent first in the
// window; afterwards, without packages, it must connect anew before it is done.
func TestPlan_AgentsUpdateThemselvesFirst(t *testing.T) {
	w := newWorld(t, agentPolicy(updates.ModeReport), 2)
	w.view("web-01").AgentTarget = "1.1.0"
	w.view("web-01").Report.Permissions.SelfUpdate = true
	actions := w.at(0).step()
	if got := w.states(); got != "web-01=8" || len(actions) != 1 || actions[0].Command.Kind != command.SelfUpdate || actions[0].Command.Version != "1.1.0" {
		t.Fatalf("start: %s %+v", got, actions)
	}
	w.at(time.Minute).finish("web-01", command.Succeeded, false)
	w.at(time.Minute + time.Second).step()
	w.at(3 * time.Minute).step()
	if n := w.run("web-01"); n.State != updates.NodeHealthCheck {
		t.Fatalf("the old session counted: %+v", n)
	}
	w.view("web-01").ConnectedAt = windowStart.Add(2 * time.Minute)
	w.at(4 * time.Minute).step()
	if n := w.run("web-01"); n.State != updates.NodeDone || w.rollout.State != updates.RolloutDone {
		t.Errorf("after the new session: %+v, %d", n, w.rollout.State)
	}
}

// With packages too: the agent first, then the updates, then the health check without a new
// session (the update restarts nothing).
func TestPlan_AgentThenPackages(t *testing.T) {
	w := newWorld(t, agentPolicy(updates.ModeAllAuto), 1)
	w.view("web-01").AgentTarget = "1.1.0"
	w.view("web-01").Report.Permissions.SelfUpdate = true
	w.at(0).step()
	w.at(time.Minute).finish("web-01", command.Succeeded, false)
	if got := commands(w.at(time.Minute + time.Second).step()); len(got) != 1 || got[0] != command.Update {
		t.Fatalf("after the agent: %v (%s)", got, w.states())
	}
	w.view("web-01").ConnectedAt = windowStart.Add(time.Minute)
	w.at(2*time.Minute).finish("web-01", command.Succeeded, false)
	w.at(2*time.Minute + time.Second).step()
	w.at(4 * time.Minute).step()
	if n := w.run("web-01"); n.State != updates.NodeDone || n.AgentRun == uuid.Nil || n.UpdateRun == uuid.Nil {
		t.Errorf("end: %+v", n)
	}
}

// A host that forbids self-updates is skipped, a failed one fails the node; no release, no rollout.
func TestPlan_AgentUpdatesSkippedFailedOrNone(t *testing.T) {
	w := newWorld(t, agentPolicy(updates.ModeReport), 2)
	w.policy.Rollout.MaxParallel = 2
	for _, h := range []string{"web-01", "web-02"} {
		w.view(h).AgentTarget = "1.1.0"
	}
	w.view("web-01").Report.Permissions.SelfUpdate = true
	w.at(0).step()
	if n := w.run("web-02"); n.State != updates.NodeSkipped || !strings.Contains(n.Reason, "allow_self_update") {
		t.Errorf("forbidden: %+v", n)
	}
	w.at(time.Minute).finish("web-01", command.Failed, false)
	w.at(time.Minute + time.Second).step()
	if n := w.run("web-01"); n.State != updates.NodeFailed || !strings.Contains(n.Reason, "agent update:") {
		t.Errorf("failed: %+v", n)
	}
	w = newWorld(t, agentPolicy(updates.ModeReport), 2)
	if r, a := updates.Plan(windowStart, w.policy, nil, w.nodes, w.runs, 10, uuid.New); r == nil || len(r.Nodes) != 0 || a != nil {
		t.Errorf("no release: %+v %+v", r, a)
	}
	// Manual agent updates in a group that installs: a node with nothing but a newer agent waits.
	installs := agentPolicy(updates.ModeAllAuto)
	installs.AgentUpdate = updates.AgentManual
	w = newWorld(t, installs, 1)
	w.view("web-01").AgentTarget, w.view("web-01").Report.Total = "1.1.0", 0
	if r, _ := updates.Plan(windowStart, w.policy, nil, w.nodes, w.runs, 10, uuid.New); r == nil || len(r.Nodes) != 0 {
		t.Errorf("manual agent updates took the node: %+v", r)
	}
	manual := agentPolicy(updates.ModeReport)
	manual.AgentUpdate = updates.AgentManual
	w = newWorld(t, manual, 1)
	w.view("web-01").AgentTarget = "1.1.0"
	if r, _ := updates.Plan(windowStart, w.policy, nil, w.nodes, w.runs, 10, uuid.New); r != nil {
		t.Errorf("manual agent updates started a rollout: %+v", r)
	}
}
