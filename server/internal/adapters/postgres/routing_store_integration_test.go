//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type routingFixture struct {
	routes   *postgres.RoutingStore
	channels *postgres.NotificationStore
	alerts   *postgres.AlertStore
	org      uuid.UUID // fresh per test: the database is shared
	ownerDSN string
	appDSN   string
}

func newRoutingFixture(t *testing.T) routingFixture {
	t.Helper()
	f := setup(t)
	db := open(t, f.appDSN)
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Routing')", org)
	return routingFixture{
		routes: postgres.NewRoutingStore(db), channels: postgres.NewNotificationStore(db), alerts: postgres.NewAlertStore(db),
		org: org, ownerDSN: f.db.OwnerDSN, appDSN: f.appDSN,
	}
}

func (f routingFixture) channel(t *testing.T, org uuid.UUID, enabled bool) notify.ChannelID {
	t.Helper()
	c := storedChannel()
	c.Enabled = enabled
	if err := f.channels.CreateChannel(context.Background(), scopeFor(t, org), c, audit("notification_channel.create")); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func routedPolicy(channels []notify.ChannelID, escalation []notify.ChannelID) routing.Policy {
	return routing.Policy{
		ID: routing.PolicyID(uuid.New()), Name: "Ops", Enabled: true, Grouping: routing.GroupDigest, Repeat: 4 * time.Hour,
		Match: routing.Matchers{
			Severities: []notify.Severity{notify.Critical}, Groups: []tenant.GroupID{tenant.GroupID(uuid.New())},
			Rules: []string{"security.*"}, Labels: map[string]string{"category": "security"},
		},
		Channels: channels, Escalation: &routing.Escalation{After: 15 * time.Minute, Channels: escalation},
		CreatedAt: notifyTime, UpdatedAt: notifyTime,
	}
}

func policyAudit(action string) access.AuditEntry {
	return access.AuditEntry{Action: action, Target: "notification_policy:x"}
}

func TestRoutingStore_PoliciesRoundTripWithAudit(t *testing.T) {
	f := newRoutingFixture(t)
	ctx, scope := context.Background(), scopeFor(t, f.org)
	a, b, c := f.channel(t, f.org, true), f.channel(t, f.org, true), f.channel(t, f.org, false)
	p := routedPolicy([]notify.ChannelID{b, a}, []notify.ChannelID{c, a})
	if err := f.routes.CreatePolicy(ctx, scope, p, policyAudit("notification_policy.create")); err != nil {
		t.Fatal(err)
	}
	list, err := f.routes.Policies(ctx, scope)
	if err != nil || len(list) != 1 {
		t.Fatalf("Policies() = %+v, %v", list, err)
	}
	got := list[0]
	// Channels keep their order; the matchers come back as stored.
	if got.ID != p.ID || got.Name != "Ops" || !got.Enabled || got.Grouping != routing.GroupDigest || got.Repeat != 4*time.Hour ||
		!slices.Equal(got.Channels, p.Channels) || got.Escalation == nil || got.Escalation.After != 15*time.Minute ||
		!slices.Equal(got.Escalation.Channels, p.Escalation.Channels) || !slices.Equal(got.Match.Groups, p.Match.Groups) ||
		!slices.Equal(got.Match.Rules, p.Match.Rules) || got.Match.Labels["category"] != "security" ||
		!slices.Equal(got.Match.Severities, p.Match.Severities) || !got.CreatedAt.Equal(notifyTime) {
		t.Errorf("policy = %+v", got)
	}
	p.Name, p.Escalation, p.Channels, p.Repeat, p.UpdatedAt = "Ops 2", nil, []notify.ChannelID{c}, 0, notifyTime.Add(time.Hour)
	if err := f.routes.UpdatePolicy(ctx, scope, p, policyAudit("notification_policy.update")); err != nil {
		t.Fatal(err)
	}
	list, _ = f.routes.Policies(ctx, scope)
	if got := list[0]; got.Name != "Ops 2" || got.Escalation != nil || !slices.Equal(got.Channels, []notify.ChannelID{c}) || got.Repeat != 0 {
		t.Errorf("updated = %+v", got)
	}
	// A deleted channel leaves the policy.
	if err := f.channels.DeleteChannel(ctx, scope, c, audit("notification_channel.delete")); err != nil {
		t.Fatal(err)
	}
	if list, _ = f.routes.Policies(ctx, scope); len(list[0].Channels) != 0 {
		t.Errorf("channels after delete = %v", list[0].Channels)
	}
	if err := f.routes.DeletePolicy(ctx, scope, p.ID, policyAudit("notification_policy.delete")); err != nil {
		t.Fatal(err)
	}
	if err := f.routes.DeletePolicy(ctx, scope, p.ID, policyAudit("notification_policy.delete")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	var audits int
	queryOwner(t, f.ownerDSN, "SELECT count(*) FROM audit_log WHERE org_id = $1 AND action LIKE 'notification_policy.%'", []any{f.org}, &audits)
	if audits != 3 {
		t.Errorf("%d audit entries, want 3", audits)
	}
}

func TestRoutingStore_KeepsOrganizationsApart(t *testing.T) {
	f := newRoutingFixture(t)
	ctx := context.Background()
	other := uuid.New()
	ownerExec(t, f.ownerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Other')", other)
	mine, theirs := f.channel(t, f.org, true), f.channel(t, other, true)
	p := routedPolicy([]notify.ChannelID{mine}, []notify.ChannelID{mine})
	if err := f.routes.CreatePolicy(ctx, scopeFor(t, f.org), p, policyAudit("notification_policy.create")); err != nil {
		t.Fatal(err)
	}
	foreign := scopeFor(t, other)
	if list, _ := f.routes.Policies(ctx, foreign); len(list) != 0 {
		t.Errorf("foreign policies: %+v", list)
	}
	if snap, _ := f.routes.Snapshot(ctx, foreign); len(snap.Policies) != 0 || len(snap.Channels) != 1 || snap.Channels[0] != theirs {
		t.Errorf("foreign snapshot: %+v", snap)
	}
	p.Name = "hijacked"
	if err := f.routes.UpdatePolicy(ctx, foreign, p, policyAudit("notification_policy.update")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("update foreign: %v", err)
	}
	if err := f.routes.DeletePolicy(ctx, foreign, p.ID, policyAudit("notification_policy.delete")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("delete foreign: %v", err)
	}
	// A policy cannot name a channel of another organization, not even by its ID.
	stolen := routedPolicy([]notify.ChannelID{mine}, []notify.ChannelID{theirs})
	if err := f.routes.CreatePolicy(ctx, scopeFor(t, f.org), stolen, policyAudit("notification_policy.create")); err == nil {
		t.Error("a policy linked a foreign channel")
	}
	if list, _ := f.routes.Policies(ctx, scopeFor(t, f.org)); len(list) != 1 || list[0].Name != "Ops" {
		t.Errorf("policies changed by another organization: %+v", list)
	}
}

// Commit queues the deliveries with the states; the states go with their instance and policy.
func TestRoutingStore_CommitAndSnapshot(t *testing.T) {
	f := newRoutingFixture(t)
	ctx, scope := context.Background(), scopeFor(t, f.org)
	node := uuid.New()
	createNode(t, f.ownerDSN, f.org, node)
	inst := firingInstance(node)
	if err := f.alerts.Save(ctx, scope, []alert.Instance{inst}, nil, nil); err != nil {
		t.Fatal(err)
	}
	on, off := f.channel(t, f.org, true), f.channel(t, f.org, false)
	p := routedPolicy([]notify.ChannelID{on}, []notify.ChannelID{off})
	if err := f.routes.CreatePolicy(ctx, scope, p, policyAudit("notification_policy.create")); err != nil {
		t.Fatal(err)
	}
	st := routing.State{StateKey: routing.StateKey{Key: inst.Key, Policy: p.ID}, LastSent: notifyTime, Escalated: true}
	d := notify.Delivery{Channel: on, Notification: notify.Notification{Kind: notify.Firing, Rule: inst.Key.Rule}, Status: notify.StatusPending, CreatedAt: notifyTime, NextAttemptAt: notifyTime}
	if err := f.routes.Commit(ctx, scope, []notify.Delivery{d}, []routing.State{st}, nil); err != nil {
		t.Fatal(err)
	}
	snap, err := f.routes.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Policies) != 1 || !slices.Equal(snap.Channels, []notify.ChannelID{on}) || len(snap.States) != 1 ||
		snap.States[0].StateKey != st.StateKey || !snap.States[0].LastSent.Equal(notifyTime) || !snap.States[0].Escalated {
		t.Errorf("snapshot %+v", snap)
	}
	if ds, _ := f.channels.Deliveries(ctx, scope, notify.DeliveryFilter{Limit: 10}); len(ds) != 1 || ds[0].Channel != on {
		t.Errorf("deliveries %+v", ds)
	}
	// Update in place, then delete.
	st.LastSent = notifyTime.Add(time.Hour)
	if err := f.routes.Commit(ctx, scope, nil, []routing.State{st}, nil); err != nil {
		t.Fatal(err)
	}
	if snap, _ = f.routes.Snapshot(ctx, scope); len(snap.States) != 1 || !snap.States[0].LastSent.Equal(st.LastSent) {
		t.Errorf("updated state %+v", snap.States)
	}
	if err := f.routes.Commit(ctx, scope, nil, nil, []routing.StateKey{st.StateKey}); err != nil {
		t.Fatal(err)
	}
	if snap, _ = f.routes.Snapshot(ctx, scope); len(snap.States) != 0 {
		t.Errorf("deleted state %+v", snap.States)
	}
	// A failing part rolls back the deliveries: a state for an instance that does not exist.
	ghost := st
	ghost.Key.Rule = "system.cpu.usage_high"
	if err := f.routes.Commit(ctx, scope, []notify.Delivery{d}, []routing.State{ghost}, nil); err == nil {
		t.Error("a state without its instance was saved")
	}
	if ds, _ := f.channels.Deliveries(ctx, scope, notify.DeliveryFilter{Limit: 10}); len(ds) != 1 {
		t.Errorf("%d deliveries after a failed commit, want 1", len(ds))
	}
	// The state ends with its instance.
	if err := f.routes.Commit(ctx, scope, nil, []routing.State{st}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.alerts.Save(ctx, scope, nil, []alert.Key{inst.Key}, []alerting.Record{}); err != nil {
		t.Fatal(err)
	}
	if snap, _ = f.routes.Snapshot(ctx, scope); len(snap.States) != 0 {
		t.Errorf("state outlived its instance: %+v", snap.States)
	}
}

func TestRoutingStore_TablesHaveRLS(t *testing.T) {
	f := newRoutingFixture(t)
	tables := []string{"notification_policies", "notification_policy_channels", "alert_routing_state"}
	for _, table := range tables {
		var enabled, forced bool
		queryOwner(t, f.ownerDSN, "SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1", []any{table}, &enabled, &forced)
		if !enabled || !forced {
			t.Errorf("%s: RLS enabled %v, forced %v", table, enabled, forced)
		}
	}
	ctx := context.Background()
	c := f.channel(t, f.org, true)
	if err := f.routes.CreatePolicy(ctx, scopeFor(t, f.org), routedPolicy([]notify.ChannelID{c}, []notify.ChannelID{c}), policyAudit("notification_policy.create")); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, table := range tables {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s without tenant: %d rows, %v", table, n, err)
		}
	}
}

type cpuInfo struct{}

func (cpuInfo) RuleInfos(context.Context, tenant.OrgID) (map[alert.RuleID]routing.RuleInfo, error) {
	return map[alert.RuleID]routing.RuleInfo{"system.cpu.usage_high": {Name: notify.Text{De: "CPU", En: "CPU"}, Unit: notify.UnitRatio}}, nil
}

// The engine fires, the router queues the notification and remembers it; an hour later the
// reminder is queued once, also when two rounds run.
func TestRouting_FromTheEngineToTheQueue(t *testing.T) {
	f := newRoutingFixture(t)
	ctx, scope := context.Background(), scopeFor(t, f.org)
	node := uuid.New()
	createNode(t, f.ownerDSN, f.org, node)
	db := open(t, f.appDSN)
	ch := f.channel(t, f.org, true)
	p := routedPolicy([]notify.ChannelID{ch}, nil)
	p.Match, p.Escalation, p.Repeat, p.Grouping = routing.Matchers{}, nil, time.Hour, routing.GroupByNode
	if err := f.routes.CreatePolicy(ctx, scope, p, policyAudit("notification_policy.create")); err != nil {
		t.Fatal(err)
	}
	now := alertTime
	router := routing.NewRouter(routing.RouterDeps{Store: f.routes, Rules: cpuInfo{}, Logger: slog.New(slog.DiscardHandler)})
	rule := alerting.Rule{ID: "system.cpu.usage_high", Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: constantRule{0.9}}
	engine := alerting.NewEngine(alerting.Deps{
		Store: postgres.NewAlertStore(db), Rules: orgRules{tenant.OrgID(f.org), rule}, Nodes: postgres.NewFleetStore(db),
		Sink: router, Now: func() time.Time { return now }, Holder: "a", Logger: slog.New(slog.DiscardHandler),
	})
	deliveries := func() []notify.Delivery {
		ds, err := f.channels.Deliveries(ctx, scope, notify.DeliveryFilter{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return ds
	}
	engine.Round(ctx)
	ds := deliveries()
	if len(ds) != 1 || ds[0].Notification.Kind != notify.Firing || ds[0].Notification.Hostname == "" || router.Stats().Errors.Load() != 0 {
		t.Fatalf("after firing: %+v, errors %d", ds, router.Stats().Errors.Load())
	}
	now = now.Add(30 * time.Minute)
	engine.Round(ctx)
	if len(deliveries()) != 1 {
		t.Errorf("a reminder before the hour")
	}
	now = now.Add(30 * time.Minute)
	engine.Round(ctx)
	engine.Round(ctx)
	ds = deliveries()
	if len(ds) != 2 || ds[0].Notification.Reason != notify.ReasonRepeat || ds[0].Notification.FiringSeconds != 3600 {
		t.Errorf("after an hour: %+v", ds)
	}
}
