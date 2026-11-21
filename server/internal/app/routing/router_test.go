package routing_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	mem  = alert.Key{Rule: "system.memory.usage_high", Node: web}
)

type commit struct {
	deliveries []notify.Delivery
	save       []routing.State
	del        []routing.StateKey
}

type fakeStore struct {
	snapshot  routing.Snapshot
	err       error
	snapshots int
	commits   []commit
}

func (f *fakeStore) Snapshot(_ context.Context, scope tenant.Scope, _ time.Time) (routing.Snapshot, error) {
	if scope.OrgID() != orgA {
		return routing.Snapshot{}, errors.New("wrong organization")
	}
	f.snapshots++
	return f.snapshot, f.err
}

func (f *fakeStore) Commit(_ context.Context, scope tenant.Scope, d []notify.Delivery, save []routing.State, del []routing.StateKey) error {
	if scope.OrgID() != orgA {
		return errors.New("wrong organization")
	}
	f.commits = append(f.commits, commit{d, save, del})
	return nil
}

type fakeRules struct{}

func (fakeRules) RuleInfos(context.Context, tenant.OrgID) (map[alert.RuleID]routing.RuleInfo, error) {
	return map[alert.RuleID]routing.RuleInfo{
		cpu.Rule: {Name: notify.Text{De: "Hohe CPU-Auslastung", En: "High CPU usage"}, Labels: map[string]string{"category": "cpu"}, Unit: notify.UnitRatio},
		mem.Rule: {Name: notify.Text{De: "Hoher Speicherverbrauch", En: "High memory usage"}, Labels: map[string]string{"category": "memory"}, Unit: notify.UnitRatio},
		ssh.Rule: {Name: notify.Text{De: "SSH-Brute-Force", En: "SSH brute force"}, Labels: map[string]string{"category": "security"}, Unit: notify.UnitCount},
	}, nil
}

type fakeUsers struct{}

func (fakeUsers) DisplayNames(context.Context, tenant.Scope) (map[identity.UserID]string, error) {
	return map[identity.UserID]string{alice: "Alice"}, nil
}

var nodes = []fleet.Node{{ID: web, Group: groupA, Hostname: "web-01"}, {ID: db, Group: groupB, Hostname: "db-01"}}

func router(s *fakeStore) *routing.Router {
	return routing.NewRouter(routing.RouterDeps{Store: s, Rules: fakeRules{}, Users: fakeUsers{}, Logger: slog.New(slog.DiscardHandler)})
}

func round(records []alerting.Record, instances ...alert.Instance) alerting.Round {
	return alerting.Round{Org: orgA, Now: t0, Records: records, Instances: instances, Nodes: nodes}
}

func TestRouter_DeliversEnrichedNotifications(t *testing.T) {
	s := &fakeStore{snapshot: routing.Snapshot{Channels: []notify.ChannelID{chanA}}}
	rec := changed(ssh, alert.StateOK, alert.StateCritical, t0)
	rec.Transition.Value, rec.Transition.Fields = 60, alert.Fields{"user": "root"}
	r := router(s)
	r.Deliver(context.Background(), round([]alerting.Record{rec}, firing(ssh, alert.StateCritical, t0)))
	if len(s.commits) != 1 || len(s.commits[0].deliveries) != 1 {
		t.Fatalf("commits %+v", s.commits)
	}
	d := s.commits[0].deliveries[0]
	n := d.Notification
	if d.Channel != chanA || d.Status != notify.StatusPending || !d.CreatedAt.Equal(t0) || !d.NextAttemptAt.Equal(t0) ||
		n.Kind != notify.Firing || n.Severity != notify.Critical || n.Rule != ssh.Rule || n.RuleName.En != "SSH brute force" ||
		n.Node != db || n.Hostname != "db-01" || n.Dimension != ssh.Dimension || n.Value != 60 || n.Unit != notify.UnitCount ||
		n.Fields["user"] != "root" || !n.At.Equal(t0) {
		t.Errorf("delivery %+v", d)
	}
	if r.Stats().Deliveries.Load() != 1 {
		t.Errorf("stats %d", r.Stats().Deliveries.Load())
	}
}

// Nothing fires and nothing changed: the router reads nothing.
func TestRouter_QuietRoundsCostNothing(t *testing.T) {
	s := &fakeStore{}
	acked := firing(cpu, alert.StateWarning, t0)
	acked.Ack = &alert.Ack{By: alice, At: t0}
	router(s).Deliver(context.Background(), round(nil, acked, alert.Instance{Key: mem, State: alert.StateResolved, Since: t0}))
	if s.snapshots != 0 || len(s.commits) != 0 {
		t.Errorf("snapshots %d commits %d", s.snapshots, len(s.commits))
	}
}

func TestRouter_GroupsPerNodeOrAsDigest(t *testing.T) {
	sec := security()
	sec.Match = routing.Matchers{}
	sec.Channels = []notify.ChannelID{chanB}
	s := &fakeStore{snapshot: routing.Snapshot{Policies: []routing.Policy{ops(), sec}, Channels: []notify.ChannelID{chanA, chanB}}}
	records := []alerting.Record{
		changed(cpu, alert.StateOK, alert.StateCritical, t0),
		changed(mem, alert.StateOK, alert.StateWarning, t0),
		changed(ssh, alert.StateOK, alert.StateWarning, t0),
	}
	router(s).Deliver(context.Background(), round(records, firing(cpu, alert.StateCritical, t0), firing(mem, alert.StateWarning, t0), firing(ssh, alert.StateWarning, t0)))
	ds := s.commits[0].deliveries
	// ops groups by node: web's two alerts in one digest, db's alone; security: one digest.
	if len(ds) != 3 {
		t.Fatalf("%d deliveries: %+v", len(ds), ds)
	}
	// In the order the groups begin.
	web01, all, db01 := ds[0].Notification, ds[1].Notification, ds[2].Notification
	if ds[0].Channel != chanA || web01.Kind != notify.KindDigest || len(web01.Items) != 2 || web01.Hostname != "web-01" {
		t.Errorf("web-01 %+v", web01)
	}
	if ds[2].Channel != chanA || db01.Kind != notify.Firing || db01.Hostname != "db-01" {
		t.Errorf("db-01 %+v", db01)
	}
	if ds[1].Channel != chanB || all.Kind != notify.KindDigest || len(all.Items) != 3 || all.Hostname != "" {
		t.Errorf("digest %+v", all)
	}
	if len(s.commits[0].save) != 6 {
		t.Errorf("save %+v", s.commits[0].save)
	}
}

// Policies name channels that may be switched off; disabled policies route nothing, but they
// keep the fallback away.
func TestRouter_OnlyEnabledChannelsAndPolicies(t *testing.T) {
	p := ops()
	p.Channels = []notify.ChannelID{chanA, chanC}
	s := &fakeStore{snapshot: routing.Snapshot{Policies: []routing.Policy{p}, Channels: []notify.ChannelID{chanA}}}
	rec := []alerting.Record{changed(cpu, alert.StateOK, alert.StateWarning, t0)}
	router(s).Deliver(context.Background(), round(rec, firing(cpu, alert.StateWarning, t0)))
	if ds := s.commits[0].deliveries; len(ds) != 1 || ds[0].Channel != chanA {
		t.Errorf("deliveries %+v", ds)
	}

	p.Enabled = false
	s = &fakeStore{snapshot: routing.Snapshot{Policies: []routing.Policy{p}, Channels: []notify.ChannelID{chanA}}}
	router(s).Deliver(context.Background(), round(rec, firing(cpu, alert.StateWarning, t0)))
	if len(s.commits) != 0 {
		t.Errorf("a disabled policy routed or let the fallback in: %+v", s.commits)
	}
}

func TestRouter_NamesWhoAcknowledged(t *testing.T) {
	s := &fakeStore{snapshot: routing.Snapshot{Channels: []notify.ChannelID{chanA}}}
	inst := firing(cpu, alert.StateWarning, t0)
	inst.Ack = &alert.Ack{By: alice, At: t0}
	ack := alerting.Record{Key: cpu, Transition: alert.Transition{Kind: alert.Acknowledged, From: alert.StateWarning, To: alert.StateWarning, At: t0, By: alice}}
	router(s).Deliver(context.Background(), round([]alerting.Record{ack}, inst))
	if n := s.commits[0].deliveries[0].Notification; n.Kind != notify.Acknowledged || n.AckBy != "Alice" {
		t.Errorf("notification %+v", n)
	}
}

func TestRouter_ReminderCarriesTheFiringTime(t *testing.T) {
	p := ops()
	p.Repeat = time.Hour
	s := &fakeStore{snapshot: routing.Snapshot{Policies: []routing.Policy{p}, Channels: []notify.ChannelID{chanA}}}
	r := round(nil, firing(cpu, alert.StateCritical, t0.Add(-2*time.Hour)))
	router(s).Deliver(context.Background(), r)
	n := s.commits[0].deliveries[0].Notification
	if n.Reason != notify.ReasonRepeat || n.FiringSeconds != 7200 || n.Severity != notify.Critical {
		t.Errorf("notification %+v", n)
	}
}

func TestRouter_CountsErrors(t *testing.T) {
	s := &fakeStore{err: errors.New("database down")}
	r := router(s)
	r.Deliver(context.Background(), round([]alerting.Record{changed(cpu, alert.StateOK, alert.StateWarning, t0)}, firing(cpu, alert.StateWarning, t0)))
	if r.Stats().Errors.Load() != 1 || len(s.commits) != 0 {
		t.Errorf("errors %d", r.Stats().Errors.Load())
	}
}

func TestRouter_SilencesFromTheSnapshotMute(t *testing.T) {
	s := &fakeStore{snapshot: routing.Snapshot{Channels: []notify.ChannelID{chanA}, Silences: []routing.Silence{webSilence()}}}
	rec := []alerting.Record{changed(cpu, alert.StateOK, alert.StateWarning, t0), changed(ssh, alert.StateOK, alert.StateWarning, t0)}
	router(s).Deliver(context.Background(), round(rec, firing(cpu, alert.StateWarning, t0), firing(ssh, alert.StateWarning, t0)))
	c := s.commits[0]
	if len(c.deliveries) != 1 || c.deliveries[0].Notification.Rule != ssh.Rule {
		t.Errorf("deliveries %+v", c.deliveries)
	}
	if !slices.ContainsFunc(c.save, func(st routing.State) bool { return st.Key == cpu && st.Muted }) {
		t.Errorf("save %+v", c.save)
	}
}
