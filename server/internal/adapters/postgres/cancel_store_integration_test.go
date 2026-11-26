//go:build integration

package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// M4-31: a pending run cancelled never goes out; a sent one is withdrawn from the agent once per
// session (again after a reconnect) and, refused by it, is cancelled; a run cancelled before
// can't be cancelled again.
func TestCancel_ARunPendingOrSent(t *testing.T) {
	f := newUpdateFixture(t, 0)
	db := open(t, setup(t).appDSN)
	ctx := context.Background()
	rs, cs := postgres.NewRolloutStore(db), postgres.NewCommandStore(db)
	now := time.Now()
	pending, sent := newCommand(f.node, command.Update, now), newCommand(f.node, command.Reboot, now)
	for _, c := range []command.Command{pending, sent} {
		if err := cs.Enqueue(ctx, f.org, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := cs.MarkSent(ctx, f.org, f.node, []uuid.UUID{sent.ID}, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{pending.ID, sent.ID} {
		if done, err := rs.CancelRun(ctx, f.org, id, identity.UserID{}, now, auditEntry("node.run_cancelled")); err != nil || !done {
			t.Fatalf("cancel %v: %v, %v", id, done, err)
		}
	}
	if done, _ := rs.CancelRun(ctx, f.org, pending.ID, identity.UserID{}, now, auditEntry("node.run_cancelled")); done {
		t.Error("cancelled twice")
	}
	if c, _, _ := cs.Get(ctx, f.org, pending.ID); c.State != command.Cancelled {
		t.Errorf("pending, cancelled: %d", c.State)
	}
	if c, _, _ := cs.Get(ctx, f.org, sent.ID); c.State != command.Sent || c.CancelledAt == nil || c.ShownState(now) != command.Cancelled {
		t.Errorf("sent, withdrawn: %+v", c)
	}
	if got, _ := cs.Deliverable(ctx, f.org, f.node, true, now); len(got) != 0 {
		t.Errorf("cancelled runs go out: %+v", got)
	}
	if got, _ := cs.Cancels(ctx, f.org, f.node, false); !slices.Equal(got, []uuid.UUID{sent.ID}) {
		t.Fatalf("cancels = %v", got)
	}
	if err := cs.MarkCancelsSent(ctx, f.org, f.node, []uuid.UUID{sent.ID}, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := cs.Cancels(ctx, f.org, f.node, false); len(got) != 0 {
		t.Errorf("the cancel goes out twice in a session: %v", got)
	}
	if got, _ := cs.Cancels(ctx, f.org, f.node, true); !slices.Equal(got, []uuid.UUID{sent.ID}) {
		t.Errorf("after a reconnect: %v", got)
	}
	if ok, err := cs.Record(ctx, f.org, f.node, sent.ID, command.Result{Phase: command.Refused, Reason: "cancelled on the server"}); err != nil || !ok {
		t.Fatalf("record = %v, %v", ok, err)
	}
	if c, _, _ := cs.Get(ctx, f.org, sent.ID); c.State != command.Cancelled {
		t.Errorf("withdrawn and refused: %d", c.State)
	}
	if got, _ := cs.Cancels(ctx, f.org, f.node, true); len(got) != 0 {
		t.Errorf("answered, still withdrawn: %v", got)
	}
	var audits int
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'node.run_cancelled'", []any{uuid.UUID(f.org)}, &audits)
	if audits != 2 {
		t.Errorf("%d audit entries", audits)
	}
}

// M4-31: a cancelled rollout stops, skips its waiting nodes and cancels the runs not started; a
// save the orchestrator planned before doesn't undo it. While the updates are paused no run goes
// out and no save sends one; what runs under way is counted for the limits.
func TestCancel_ARolloutAndThePause(t *testing.T) {
	f := newUpdateFixture(t, 0)
	db := open(t, setup(t).appDSN)
	ctx := context.Background()
	rs, cs, ps := postgres.NewRolloutStore(db), postgres.NewCommandStore(db), postgres.NewUpdatePolicyStore(db)
	group := nodeGroup(t, uuid.UUID(f.org), uuid.UUID(f.node))
	now := time.Now()
	run := newCommand(f.node, command.Update, now)
	r := updates.RolloutRun{
		ID: uuid.New(), Group: group, WindowStart: now, WindowEnd: now.Add(time.Hour), State: updates.RolloutRunning,
		Nodes: []updates.NodeRun{
			{Node: f.node, Hostname: "a", State: updates.NodeUpdating, UpdateRun: run.ID, Since: now},
			{Node: f.peer, Hostname: "b", State: updates.NodeWaiting, Since: now},
		},
	}
	if held, err := rs.Lease(ctx, f.org, "me", time.Minute); err != nil || !held {
		t.Fatalf("lease: %v, %v", held, err)
	}
	if err := rs.Save(ctx, f.org, "me", now, []updates.Change{{Rollout: r, Actions: []updates.Action{{Command: &run, Node: f.node}}}}); err != nil {
		t.Fatal(err)
	}
	if done, err := rs.CancelRollout(ctx, f.org, r.ID, identity.UserID{}, now, auditEntry("updates.cancel")); err != nil || !done {
		t.Fatalf("cancel: %v, %v", done, err)
	}
	if done, _ := rs.CancelRollout(ctx, f.org, r.ID, identity.UserID{}, now, auditEntry("updates.cancel")); done {
		t.Error("a stopped rollout cancelled again")
	}
	states := func() string {
		var s string
		queryOwner(t, setup(t).db.OwnerDSN, `SELECT (SELECT state::text FROM update_rollouts WHERE id = $1) || ':' ||
			(SELECT string_agg(hostname || '=' || state, ',' ORDER BY position) FROM update_rollout_nodes WHERE rollout_id = $1)`, []any{r.ID}, &s)
		return s
	}
	if got := states(); got != "3:a=2,b=7" {
		t.Errorf("after the cancel: %s", got)
	}
	if c, _, _ := cs.Get(ctx, f.org, run.ID); c.State != command.Cancelled {
		t.Errorf("the rollout's pending run: %d", c.State)
	}
	// The orchestrator planned without the cancel: its save is dropped, a new run doesn't go out.
	stale := r
	stale.Nodes = []updates.NodeRun{r.Nodes[0], {Node: f.peer, Hostname: "b", State: updates.NodeUpdating, UpdateRun: uuid.New(), Since: now}}
	next := newCommand(f.peer, command.Update, now)
	next.ID = stale.Nodes[1].UpdateRun
	if err := rs.Save(ctx, f.org, "me", now, []updates.Change{{Rollout: stale, Actions: []updates.Action{{Command: &next, Node: f.peer}}}}); err != nil {
		t.Fatal(err)
	}
	if got := states(); got != "3:a=2,b=7" {
		t.Errorf("the stale save undid the cancel: %s", got)
	}
	if _, found, _ := cs.Get(ctx, f.org, next.ID); found {
		t.Error("the stale save sent a run")
	}
	loaded, ok, err := rs.Rollout(ctx, scopeFor(t, uuid.UUID(f.org)), r.ID)
	if err != nil || !ok || loaded.State != updates.RolloutStopped || loaded.Group != group {
		t.Errorf("rollout = %+v, %v", loaded, err)
	}

	// Paused: a pending update doesn't go out, a refresh does; a save that sends a run is dropped.
	if err := ps.SetPaused(ctx, f.org, &now, identity.UserID{}, auditEntry("updates.pause")); err != nil {
		t.Fatal(err)
	}
	if at, err := ps.PausedSince(ctx, f.org); err != nil || at == nil {
		t.Fatalf("paused since %v, %v", at, err)
	}
	update, refresh := newCommand(f.peer, command.Update, now), newCommand(f.peer, command.Refresh, now)
	for _, c := range []command.Command{update, refresh} {
		if err := cs.Enqueue(ctx, f.org, c); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := cs.Deliverable(ctx, f.org, f.peer, false, now); len(got) != 1 || got[0].ID != refresh.ID {
		t.Errorf("deliverable while paused = %+v", got)
	}
	fresh := updates.RolloutRun{ID: uuid.New(), Group: group, WindowStart: now.Add(time.Minute), WindowEnd: now.Add(time.Hour), State: updates.RolloutRunning}
	third := newCommand(f.peer, command.Update, now)
	if err := rs.Save(ctx, f.org, "me", now, []updates.Change{{Rollout: fresh, Actions: []updates.Action{{Command: &third, Node: f.peer}}}}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := cs.Get(ctx, f.org, third.ID); found {
		t.Error("a run went out while paused")
	}
	reboot := newCommand(f.node, command.Reboot, now)
	if err := cs.Enqueue(ctx, f.org, reboot); err != nil {
		t.Fatal(err)
	}
	w, err := rs.UnderWay(ctx, f.org, []identity.NodeID{f.node, f.peer})
	if err != nil || !w.Paused || w.Org != 2 || w.Instance < w.Org || !w.Rebooting[f.node] || w.Rebooting[f.peer] || !w.InRollout[f.node] {
		t.Errorf("under way = %+v, %v", w, err)
	}
	if err := ps.SetPaused(ctx, f.org, nil, identity.UserID{}, auditEntry("updates.resume")); err != nil {
		t.Fatal(err)
	}
	if got, _ := cs.Deliverable(ctx, f.org, f.peer, false, now); len(got) != 2 {
		t.Errorf("deliverable after the resume = %+v", got)
	}
}
