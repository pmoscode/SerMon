//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var alertTime = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func firingInstance(node uuid.UUID) alert.Instance {
	at := alertTime
	return alert.Instance{
		Key:   alert.Key{Rule: "system.disk.usage_high", Node: identity.NodeID(node), Dimension: "mountpoint=/var\x1f"},
		State: alert.StateCritical, Since: at.Add(time.Minute), FiredAt: at, Flapping: true, Value: 0.97,
		Ack:    &alert.Ack{By: identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1")), At: at.Add(2 * time.Minute)},
		Flips:  []time.Time{at, at.Add(30 * time.Second)},
		Fields: alert.Fields{"user": "root", "ip": "192.0.2.7"},
	}
}

func TestAlertStore_SavesAndLoadsInstancesWithTheirHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	store := postgres.NewAlertStore(open(t, f.appDSN))
	inst := firingInstance(node)
	record := alerting.Record{Key: inst.Key, Transition: alert.Transition{Kind: alert.StateChanged, From: alert.StateWarning, To: alert.StateCritical, At: inst.Since, Value: 0.97, Fields: inst.Fields}}

	if err := store.Save(ctx, scopeFor(t, orgA), []alert.Instance{inst}, nil, []alerting.Record{record}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Instances(ctx, scopeFor(t, orgA))
	if err != nil {
		t.Fatal(err)
	}
	var got *alert.Instance
	for i := range loaded {
		if loaded[i].Key == inst.Key {
			got = &loaded[i]
		}
	}
	if got == nil || got.State != inst.State || !got.Since.Equal(inst.Since) || !got.FiredAt.Equal(inst.FiredAt) ||
		!got.PendingSince.IsZero() || got.Ack == nil || got.Ack.By != inst.Ack.By || !got.Flapping || got.Value != 0.97 ||
		len(got.Flips) != 2 || got.Check() != "" || got.Fields["user"] != "root" || got.Fields["ip"] != "192.0.2.7" {
		t.Fatalf("loaded %+v, want %+v", got, inst)
	}
	history, err := store.History(ctx, scopeFor(t, orgA), inst.Key, 10)
	if err != nil || len(history) != 1 || history[0].Transition.To != alert.StateCritical || history[0].Transition.Value != 0.97 ||
		history[0].Transition.Fields["ip"] != "192.0.2.7" {
		t.Errorf("History() = %+v, %v", history, err)
	}

	// Update and remove; metric rules have no fields.
	inst.State, inst.Ack, inst.Fields = alert.StateWarning, nil, nil
	if err := store.Save(ctx, scopeFor(t, orgA), []alert.Instance{inst}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if loaded, _ := store.Instances(ctx, scopeFor(t, orgA)); !hasInstance(loaded, inst.Key, alert.StateWarning) {
		t.Error("update not stored")
	}
	if err := store.Save(ctx, scopeFor(t, orgA), nil, []alert.Key{inst.Key}, nil); err != nil {
		t.Fatal(err)
	}
	if loaded, _ := store.Instances(ctx, scopeFor(t, orgA)); hasInstance(loaded, inst.Key, alert.StateWarning) {
		t.Error("removed instance still there")
	}
}

func hasInstance(list []alert.Instance, key alert.Key, state alert.State) bool {
	for _, i := range list {
		if i.Key == key && i.State == state {
			return true
		}
	}
	return false
}

func TestAlertStore_KeepsOrganizationsApart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	nodeA, nodeB := uuid.New(), uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, nodeA)
	createNode(t, f.db.OwnerDSN, orgB, nodeB)
	store := postgres.NewAlertStore(open(t, f.appDSN))
	instA := firingInstance(nodeA)
	if err := store.Save(ctx, scopeFor(t, orgA), []alert.Instance{instA}, nil,
		[]alerting.Record{{Key: instA.Key, Transition: alert.Transition{Kind: alert.StateChanged, To: alert.StateCritical, At: alertTime}}}); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Instances(ctx, scopeFor(t, orgB))
	if err != nil || hasInstance(loaded, instA.Key, instA.State) {
		t.Errorf("org B sees the alerts of org A: %v", err)
	}
	if h, _ := store.History(ctx, scopeFor(t, orgB), instA.Key, 10); len(h) != 0 {
		t.Error("org B sees the history of org A")
	}
	// Writing into another organization fails: the node belongs to org B, the scope to org A.
	if err := store.Save(ctx, scopeFor(t, orgA), []alert.Instance{firingInstance(nodeB)}, nil, nil); err == nil {
		t.Error("an instance for a node of another organization was stored")
	}
}

// The history is the record of what happened: the application may append, not rewrite.
func TestAlertStore_HistoryIsAppendOnlyForTheApplication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', $1, false)", orgA.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "UPDATE alert_history SET value = 0"); err == nil {
		t.Error("UPDATE must be denied for sermon_app")
	}
	// DELETE reaches only entries older than a year (TestAlertStore_HistoryRetention).
	var recent int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM alert_history WHERE at >= now() - interval '365 days'").Scan(&recent); err != nil {
		t.Fatal(err)
	}
	if tag, err := conn.Exec(ctx, "DELETE FROM alert_history WHERE at >= now() - interval '365 days'"); err != nil || tag.RowsAffected() != 0 {
		t.Errorf("deleting the recent history: %d of %d rows, %v", tag.RowsAffected(), recent, err)
	}
}

func TestAlertStore_LeaseHasOneHolderUntilItExpires(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAlertStore(open(t, f.appDSN))
	org := tenant.OrgID(orgA)
	ownerExec(t, f.db.OwnerDSN, "DELETE FROM alert_leases")

	// Concurrent first leases: one winner.
	var wg sync.WaitGroup
	wins := make(chan string, 8)
	for i := range 8 {
		holder := string(rune('a' + i))
		wg.Go(func() {
			if ok, err := store.Lease(ctx, org, holder, alertTime, alertTime.Add(30*time.Second)); err == nil && ok {
				wins <- holder
			}
		})
	}
	wg.Wait()
	close(wins)
	var winners []string
	for w := range wins {
		winners = append(winners, w)
	}
	if len(winners) != 1 {
		t.Fatalf("%d winners (%v), want 1", len(winners), winners)
	}
	holder, other := winners[0], "z"
	if ok, _ := store.Lease(ctx, org, other, alertTime.Add(10*time.Second), alertTime.Add(40*time.Second)); ok {
		t.Error("another holder got a held lease")
	}
	if ok, _ := store.Lease(ctx, org, holder, alertTime.Add(10*time.Second), alertTime.Add(40*time.Second)); !ok {
		t.Error("the holder must be able to renew")
	}
	if ok, _ := store.Lease(ctx, org, other, alertTime.Add(41*time.Second), alertTime.Add(71*time.Second)); !ok {
		t.Error("an expired lease must be taken over")
	}

	orgs, err := store.Organizations(ctx)
	if err != nil || len(orgs) < 2 {
		t.Errorf("Organizations() = %v, %v", orgs, err)
	}
}

type constantRule struct{ value float64 }

func (c constantRule) Evaluate(_ context.Context, env alerting.Env) ([]alerting.Result, error) {
	th := alert.Thresholds{Warning: &alert.Condition{Op: alert.Above, Value: 0.85}}
	out := make([]alerting.Result, 0, len(env.Nodes))
	for _, n := range env.Nodes {
		out = append(out, alerting.Result{Node: n.ID, Observation: th.Evaluate(c.value)})
	}
	return out, nil
}

type orgRules struct {
	org  tenant.OrgID
	rule alerting.Rule
}

func (o orgRules) Rules(_ context.Context, org tenant.OrgID) ([]alerting.Rule, error) {
	if org != o.org {
		return nil, nil
	}
	return []alerting.Rule{o.rule}, nil
}

// Two server instances on one database: one evaluates, the state survives the takeover, and the
// alerts land only in the organization of the node.
func TestAlertEngine_TwoInstancesShareTheWorkThroughThePostgresLease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Alerting')", org)
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, org, node)
	db := open(t, f.appDSN)
	now := alertTime
	clock := func() time.Time { return now }
	rule := alerting.Rule{ID: "system.cpu.usage_high", Timing: alert.Timing{For: 20 * time.Second, Flapping: alert.DefaultFlapping}, Evaluator: constantRule{0.9}}
	engine := func(holder string) *alerting.Engine {
		return alerting.NewEngine(alerting.Deps{
			Store: postgres.NewAlertStore(db), Rules: orgRules{tenant.OrgID(org), rule}, Nodes: postgres.NewFleetStore(db),
			Now: clock, Holder: holder, Logger: slog.New(slog.DiscardHandler),
		})
	}
	a, b := engine("instance-a"), engine("instance-b")
	key := alert.Key{Rule: rule.ID, Node: identity.NodeID(node)}
	state := func() alert.State {
		list, err := postgres.NewAlertStore(db).Instances(ctx, scopeFor(t, org))
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range list {
			if i.Key == key {
				return i.State
			}
		}
		return alert.StateOK
	}

	a.Round(ctx)
	b.Round(ctx)
	if state() != alert.StatePending || a.Stats().Evaluations.Load() != 1 || b.Stats().Evaluations.Load() != 0 {
		t.Fatalf("first round: %v, evaluations a=%d b=%d", state(), a.Stats().Evaluations.Load(), b.Stats().Evaluations.Load())
	}
	// a stops; b takes over once the lease expired and fires at the original deadline.
	now = now.Add(alerting.LeaseDuration)
	b.Round(ctx)
	if state() != alert.StateWarning {
		t.Errorf("after the takeover: %v, want warning", state())
	}
	if h, _ := postgres.NewAlertStore(db).History(ctx, scopeFor(t, org), key, 10); len(h) != 2 {
		t.Errorf("history = %d entries, want pending and warning", len(h))
	}
	if list, _ := postgres.NewAlertStore(db).Instances(ctx, scopeFor(t, orgB)); hasInstance(list, key, alert.StateWarning) {
		t.Error("the alert leaked into another organization")
	}
}

func ackRecord(inst alert.Instance, by identity.UserID, at time.Time) alerting.Record {
	return alerting.Record{Key: inst.Key, Transition: alert.Transition{Kind: alert.Acknowledged, From: inst.State, To: inst.State, At: at, Value: inst.Value, By: by}}
}

// The API acknowledges while the engine evaluates: the engine's save keeps the acknowledgement
// unless the alert escalated or ended.
func TestAlertStore_AcknowledgementSurvivesTheEnginesSave(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Ack')", org)
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, org, node)
	store := postgres.NewAlertStore(open(t, f.appDSN))
	scope := scopeFor(t, org)
	by := identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	inst := firingInstance(node)
	inst.State, inst.Ack, inst.Flapping = alert.StateWarning, nil, false
	if err := store.Save(ctx, scope, []alert.Instance{inst}, nil, nil); err != nil {
		t.Fatal(err)
	}
	at := alertTime.Add(5 * time.Minute)
	if err := store.Acknowledge(ctx, scope, inst.Key, alert.Ack{By: by, At: at}, ackRecord(inst, by, at)); err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(ctx, scope, inst.Key, alert.Ack{By: by, At: at}, ackRecord(inst, by, at)); !errors.Is(err, alert.ErrNotFiring) {
		t.Errorf("second acknowledgement: %v", err)
	}
	ack := func() *alert.Ack {
		list, err := store.Instances(ctx, scope)
		if err != nil || len(list) != 1 {
			t.Fatalf("instances %+v, %v", list, err)
		}
		return list[0].Ack
	}
	// The engine saves its older copy (without the acknowledgement): it stays.
	stale := inst
	stale.Value = 0.99
	if err := store.Save(ctx, scope, []alert.Instance{stale}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if a := ack(); a == nil || a.By != by || !a.At.Equal(at) {
		t.Fatalf("acknowledgement lost: %+v", a)
	}
	// Escalation to critical ends it.
	stale.State = alert.StateCritical
	if err := store.Save(ctx, scope, []alert.Instance{stale}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if a := ack(); a != nil {
		t.Errorf("acknowledgement after escalation: %+v", a)
	}
	// So does a new firing (another fired_at).
	if err := store.Acknowledge(ctx, scope, inst.Key, alert.Ack{By: by, At: at}, ackRecord(stale, by, at)); err != nil {
		t.Fatal(err)
	}
	refired := stale
	refired.FiredAt = alertTime.Add(time.Hour)
	if err := store.Save(ctx, scope, []alert.Instance{refired}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if a := ack(); a != nil {
		t.Errorf("acknowledgement after a new firing: %+v", a)
	}
	if h, _ := store.History(ctx, scope, inst.Key, 10); len(h) != 2 || h[0].Transition.Kind != alert.Acknowledged || h[0].Transition.By != by {
		t.Errorf("history %+v", h)
	}
	// Only firing alerts: a resolved one is refused.
	refired.State, refired.FiredAt = alert.StateResolved, time.Time{}
	if err := store.Save(ctx, scope, []alert.Instance{refired}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(ctx, scope, inst.Key, alert.Ack{By: by, At: at}, ackRecord(refired, by, at)); !errors.Is(err, alert.ErrNotFiring) {
		t.Errorf("acknowledging a resolved alert: %v", err)
	}
}

// The history is kept a year; the application deletes nothing younger, not even directly.
func TestAlertStore_HistoryRetention(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Retention')", org)
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, org, node)
	store := postgres.NewAlertStore(open(t, f.appDSN))
	scope := scopeFor(t, org)
	inst := firingInstance(node)
	now := time.Now().UTC()
	rec := func(at time.Time) alerting.Record {
		return alerting.Record{Key: inst.Key, Transition: alert.Transition{Kind: alert.StateChanged, From: alert.StateOK, To: alert.StateWarning, At: at, Value: 1}}
	}
	if err := store.Save(ctx, scope, nil, nil, []alerting.Record{rec(now.AddDate(-2, 0, 0)), rec(now.AddDate(0, -6, 0)), rec(now)}); err != nil {
		t.Fatal(err)
	}
	// A purge that asks for too much deletes only what the policy allows.
	n, err := store.PurgeHistory(ctx, scope, now.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PurgeHistory() = %d, %v", n, err)
	}
	if h, _ := store.History(ctx, scope, inst.Key, 10); len(h) != 2 {
		t.Errorf("history after the purge: %d entries", len(h))
	}
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', $1, false)", org.String()); err != nil {
		t.Fatal(err)
	}
	tag, err := conn.Exec(ctx, "DELETE FROM alert_history")
	if err != nil || tag.RowsAffected() != 0 {
		t.Errorf("direct delete of the recent history: %v rows, %v", tag.RowsAffected(), err)
	}
}
