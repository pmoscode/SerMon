//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// M4-13 over the database: two instances, one rollout; a node through update and health check, a
// failing one stops the rollout with its server events; reboot and offline alerts are muted.
func TestOrchestrator_RollsOutOverTheDatabase(t *testing.T) {
	f := newUpdateFixture(t, 0)
	db := open(t, setup(t).appDSN)
	ctx := context.Background()
	scope := scopeFor(t, uuid.UUID(f.org))
	group := nodeGroup(t, uuid.UUID(f.org), uuid.UUID(f.node))
	// Both nodes into one group.
	ownerExec(t, setup(t).db.OwnerDSN, "UPDATE nodes SET group_id = $1, last_seen_at = now(), connected_at = now() - interval '1 hour' WHERE id IN ($2, $3)",
		uuid.UUID(group), uuid.UUID(f.node), uuid.UUID(f.peer))
	ownerExec(t, setup(t).db.OwnerDSN, "UPDATE nodes SET hostname = 'a-node' WHERE id = $1", uuid.UUID(f.node))
	ownerExec(t, setup(t).db.OwnerDSN, "UPDATE nodes SET hostname = 'b-node' WHERE id = $1", uuid.UUID(f.peer))
	reports := postgres.NewUpdateStore(db, 0)
	report := func(node identity.NodeID, at time.Time, total uint32) {
		r := update.Report{
			CollectedAt: at, Availability: update.Listed, Manager: update.Apt, Total: total,
			Permissions: &update.Permissions{RemoteUpdates: true},
		}
		if _, err := reports.Write(ctx, f.org, node, r, at); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	report(f.node, start.Add(-time.Hour), 2)
	report(f.peer, start.Add(-time.Hour), 3)
	policy := updates.Policy{
		Group: group, Mode: updates.ModeAllAuto, UpdatedAt: start,
		Window:  &routing.Schedule{Days: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, Start: 0, Duration: 24 * time.Hour, TimeZone: "UTC"},
		Rollout: updates.Rollout{MaxParallel: 1, StopOnFailure: true, HealthCheck: 5 * time.Minute},
	}
	if err := postgres.NewUpdatePolicyStore(db).PutPolicy(ctx, scope, group, &policy, auditEntry("update_policy.update")); err != nil {
		t.Fatal(err)
	}
	now := start
	clock := func() time.Time { return now }
	instance := func(holder string) *updates.Orchestrator {
		c := updates.DefaultOrchestratorConfig(holder, slog.New(slog.DiscardHandler))
		c.Now = clock
		return updates.NewOrchestrator(postgres.NewRolloutStore(open(t, setup(t).appDSN)), c)
	}
	a, b := instance("instance-a-"+f.org.String()[:8]), instance("instance-b-"+f.org.String()[:8])
	a.Round(ctx)
	b.Round(ctx)
	commands := func(kind command.Kind) []uuid.UUID {
		t.Helper()
		var ids []uuid.UUID
		queryAll(t, setup(t).db.OwnerDSN, "SELECT id FROM node_commands WHERE org_id = $1 AND kind = $2 ORDER BY created_at",
			[]any{uuid.UUID(f.org), int(kind)}, func(scan func(...any) error) {
				var id uuid.UUID
				if err := scan(&id); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			})
		return ids
	}
	if got := commands(command.Update); len(got) != 1 {
		t.Fatalf("%d update commands after both instances, want one (maxParallel 1, one lease)", len(got))
	}
	if b.Stats().Started.Load() != 0 || a.Stats().Started.Load() != 1 {
		t.Errorf("started a=%d b=%d", a.Stats().Started.Load(), b.Stats().Started.Load())
	}
	var silences int
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT count(*) FROM alert_silences WHERE org_id = $1 AND $2 = ANY (node_ids)", []any{uuid.UUID(f.org), uuid.UUID(f.node)}, &silences)
	if silences != 1 {
		t.Errorf("%d silences for the node", silences)
	}
	// The first node (a-node) succeeds; its report after the update has nothing left.
	cmds := postgres.NewCommandStore(db)
	run := commands(command.Update)[0]
	ended := now.Add(time.Minute)
	if ok, err := cmds.Record(ctx, f.org, f.node, run, command.Result{Phase: command.Succeeded, EndedAt: &ended}); err != nil || !ok {
		t.Fatalf("record = %v, %v", ok, err)
	}
	now = now.Add(2 * time.Minute)
	report(f.node, now, 0)
	ownerExec(t, setup(t).db.OwnerDSN, "UPDATE nodes SET last_seen_at = $2 WHERE id IN ($1, $3)", uuid.UUID(f.node), now, uuid.UUID(f.peer))
	a.Round(ctx) // update done: health check
	now = now.Add(updates.Settle)
	ownerExec(t, setup(t).db.OwnerDSN, "UPDATE nodes SET last_seen_at = $2 WHERE id IN ($1, $3)", uuid.UUID(f.node), now, uuid.UUID(f.peer))
	a.Round(ctx) // healthy: done; b-node starts
	if got := commands(command.Update); len(got) != 2 {
		t.Fatalf("%d update commands, want the second node's now", len(got))
	}
	// The second fails: the rollout stops, with both server events.
	if ok, _ := cmds.Record(ctx, f.org, f.peer, commands(command.Update)[1], command.Result{Phase: command.Failed, ExitCode: 100}); !ok {
		t.Fatal("result of the second node not taken")
	}
	now = now.Add(time.Minute)
	a.Round(ctx)
	var state int
	var reason string
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT state, reason FROM update_rollouts WHERE org_id = $1", []any{uuid.UUID(f.org)}, &state, &reason)
	if state != int(updates.RolloutStopped) || !strings.Contains(reason, "b-node: update: failed with exit code 100") {
		t.Errorf("rollout = %d %q", state, reason)
	}
	var events string
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT string_agg(kind, ',' ORDER BY kind) FROM security_events WHERE org_id = $1 AND node_id = $2",
		[]any{uuid.UUID(f.org), uuid.UUID(f.peer)}, &events)
	if events != "updates.rollout_stopped,updates.run_failed" {
		t.Errorf("events = %q", events)
	}
	var states string
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT string_agg(hostname || '=' || state, ',' ORDER BY position) FROM update_rollout_nodes WHERE org_id = $1",
		[]any{uuid.UUID(f.org)}, &states)
	if states != "a-node=5,b-node=6" {
		t.Errorf("node states = %s", states)
	}
	// Another round in the same window starts no second rollout.
	a.Round(ctx)
	var rollouts int
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT count(*) FROM update_rollouts WHERE org_id = $1", []any{uuid.UUID(f.org)}, &rollouts)
	if rollouts != 1 {
		t.Errorf("%d rollouts", rollouts)
	}
}

func auditEntry(action string) access.AuditEntry {
	return access.AuditEntry{Action: action, Target: "test"}
}

// queryAll runs sql as the owner and calls each with the scan of every row.
func queryAll(t *testing.T, dsn, sql string, args []any, each func(scan func(...any) error)) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	rows, err := conn.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		each(rows.Scan)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// What the rollout reads of a node: critical alerts count, except offline and rebooted (muted
// during the rollout); a save without the lease changes nothing (fencing).
func TestRolloutStore_ReadsCriticalAlertsAndFencesTheSave(t *testing.T) {
	f := newUpdateFixture(t, 0)
	db := open(t, setup(t).appDSN)
	ctx := context.Background()
	store := postgres.NewRolloutStore(db)
	group := nodeGroup(t, uuid.UUID(f.org), uuid.UUID(f.node))
	policy := updates.Policy{
		Group: group, Mode: updates.ModeAllAuto, UpdatedAt: time.Now(),
		Window:  &routing.Schedule{Days: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, Start: 0, Duration: 24 * time.Hour, TimeZone: "UTC"},
		Rollout: updates.Rollout{MaxParallel: 1, HealthCheck: 5 * time.Minute},
	}
	if err := postgres.NewUpdatePolicyStore(db).PutPolicy(ctx, scopeFor(t, uuid.UUID(f.org)), group, &policy, auditEntry("update_policy.update")); err != nil {
		t.Fatal(err)
	}
	owner := setup(t).db.OwnerDSN
	for rule, counts := range map[string]bool{"system.cpu.usage_high": true, "system.node.offline": false, "system.node.rebooted": false} {
		// Offline and rebooted are muted during a rollout and don't count.
		ownerExec(t, owner, "DELETE FROM alert_instances WHERE org_id = $1", uuid.UUID(f.org))
		ownerExec(t, owner, "INSERT INTO alert_instances (org_id, rule_id, node_id, state, since) VALUES ($1, $2, $3, 3, now())",
			uuid.UUID(f.org), rule, uuid.UUID(f.node))
		state, err := store.Load(ctx, f.org, time.Now())
		if err != nil || len(state.Groups) != 1 {
			t.Fatalf("Load() = %+v, %v", state, err)
		}
		for _, n := range state.Groups[0].Nodes {
			if n.ID == f.node && n.Critical != counts {
				t.Errorf("%s: critical = %v", rule, n.Critical)
			}
		}
	}
	now := time.Now()
	if ok, err := store.Lease(ctx, f.org, "holder-a", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("lease = %v, %v", ok, err)
	}
	if ok, _ := store.Lease(ctx, f.org, "holder-b", now, now.Add(time.Minute)); ok {
		t.Fatal("a second holder got the lease")
	}
	change := updates.Change{Rollout: updates.RolloutRun{
		ID: uuid.New(), Group: group, WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(time.Hour), State: updates.RolloutRunning,
	}}
	if err := store.Save(ctx, f.org, "holder-b", now, []updates.Change{change}); !errors.Is(err, updates.ErrLeaseLost) {
		t.Errorf("save without the lease = %v", err)
	}
	if err := store.Save(ctx, f.org, "holder-a", now, []updates.Change{change}); err != nil {
		t.Errorf("save with the lease = %v", err)
	}
}
