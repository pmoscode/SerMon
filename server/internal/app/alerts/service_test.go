package alerts_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	t0     = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	groupA = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	groupB = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
	web    = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	db     = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	alice  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000e1"))
	cpu    = alert.Key{Rule: "system.cpu.usage_high", Node: web}
	ssh    = alert.Key{Rule: "security.ssh.bruteforce", Node: db, Dimension: "ip=192.0.2.7\x1f"}
	disk   = alert.Key{Rule: "system.filesystem.full_soon", Node: web, Dimension: "mountpoint=/var\x1f"}
	mem    = alert.Key{Rule: "system.memory.usage_high", Node: web}
)

// store holds orgA's instances and history.
type store struct {
	instances []alert.Instance
	history   []alerting.Record
	acks      []alerting.Record
}

func (s *store) Instances(_ context.Context, scope tenant.Scope) ([]alert.Instance, error) {
	if scope.OrgID() != orgA {
		return nil, nil
	}
	return slices.Clone(s.instances), nil
}

func (s *store) History(_ context.Context, scope tenant.Scope, key alert.Key, limit int32) ([]alerting.Record, error) {
	var out []alerting.Record
	for _, r := range s.history {
		if scope.OrgID() == orgA && r.Key == key && len(out) < int(limit) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *store) Acknowledge(_ context.Context, scope tenant.Scope, key alert.Key, ack alert.Ack, rec alerting.Record) error {
	for i := range s.instances {
		if scope.OrgID() == orgA && s.instances[i].Key == key && s.instances[i].State.Firing() && s.instances[i].Ack == nil {
			s.instances[i].Ack = &ack
			s.acks = append(s.acks, rec)
			return nil
		}
	}
	return alert.ErrNotFiring
}

type directory struct{}

var nodes = []fleet.Node{{ID: web, Group: groupA, GroupName: "web", Hostname: "web-01"}, {ID: db, Group: groupB, GroupName: "db", Hostname: "db-01"}}

func (directory) List(_ context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	var out []fleet.Node
	for _, n := range nodes {
		if scope.OrgID() == orgA && scope.AllowsGroup(n.Group) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (directory) HasGroup(context.Context, tenant.Scope, tenant.GroupID) (bool, error) {
	return true, nil
}

type rules struct{}

func (rules) RuleInfos(context.Context, tenant.OrgID) (map[alert.RuleID]routing.RuleInfo, error) {
	return map[alert.RuleID]routing.RuleInfo{
		cpu.Rule:  {Name: notify.Text{De: "Hohe CPU-Auslastung", En: "High CPU usage"}, Labels: map[string]string{"category": "cpu"}, Unit: notify.UnitRatio},
		ssh.Rule:  {Name: notify.Text{De: "SSH-Brute-Force", En: "SSH brute force"}, Labels: map[string]string{"category": "security"}, Unit: notify.UnitCount},
		disk.Rule: {Name: notify.Text{De: "Dateisystem bald voll", En: "File system full soon"}, Unit: notify.UnitHours},
	}, nil
}

type users struct{}

func (users) DisplayNames(context.Context, tenant.Scope) (map[identity.UserID]string, error) {
	return map[identity.UserID]string{alice: "Alice"}, nil
}

type mutes struct{ silences []routing.Silence }

func (m mutes) Silences(_ context.Context, _ tenant.Scope, since time.Time) ([]routing.Silence, error) {
	var out []routing.Silence
	for _, s := range m.silences {
		if s.EndsAt.After(since) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (mutes) Windows(context.Context, tenant.Scope) ([]routing.MaintenanceWindow, error) {
	return nil, nil
}

type sink struct{ rounds []alerting.Round }

func (s *sink) Deliver(_ context.Context, r alerting.Round) { s.rounds = append(s.rounds, r) }

type fixture struct {
	store *store
	sink  *sink
	svc   *alerts.Service
}

func newFixture(now time.Time, silences ...routing.Silence) fixture {
	return newFixtureWith(authz.NewPolicy(), now, silences...)
}

func newFixtureWith(policy authz.Policy, now time.Time, silences ...routing.Silence) fixture {
	acked := alert.Instance{Key: mem, State: alert.StateWarning, Since: t0, FiredAt: t0, Ack: &alert.Ack{By: alice, At: t0.Add(time.Minute)}}
	f := fixture{store: &store{instances: []alert.Instance{
		acked, // stored first, listed after the other warning
		{Key: cpu, State: alert.StateCritical, Since: t0, FiredAt: t0.Add(-time.Hour), Value: 0.97},
		{Key: ssh, State: alert.StateWarning, Since: t0, FiredAt: t0, Value: 12, Fields: alert.Fields{"user": "root", "ip": "192.0.2.7"}},
		{Key: disk, State: alert.StatePending, Since: t0, PendingSince: t0, Value: 5},
		{Key: alert.Key{Rule: "system.swap.usage_high", Node: web}, State: alert.StateOK, Since: t0, Flips: []time.Time{t0}},
	}}, sink: &sink{}}
	f.svc = alerts.NewService(alerts.Deps{
		Store: f.store, Nodes: directory{}, Rules: rules{}, Users: users{}, Mutes: mutes{silences}, Sink: f.sink,
		Policy: policy, Now: func() time.Time { return now },
	})
	return f
}

func caller(org tenant.OrgID, b authz.Binding, stepUp time.Time) access.Caller {
	return access.Caller{Principal: authz.Principal{User: alice, Org: org, Bindings: []authz.Binding{b}}, StepUpAt: stepUp}
}

func all(r role.Role) authz.Binding                  { return authz.Binding{AllGroups: true, Role: r} }
func in(g tenant.GroupID, r role.Role) authz.Binding { return authz.Binding{Group: g, Role: r} }

func keys(list []alerts.Alert) []alert.Key {
	out := make([]alert.Key, len(list))
	for i, a := range list {
		out[i] = a.Key
	}
	return out
}

func TestList_ActiveAlertsMostUrgentFirst(t *testing.T) {
	list, err := newFixture(t0).svc.List(context.Background(), caller(orgA, all(role.Operator), time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	// Critical, then warning (the acknowledged one last among them), then pending; OK is not active.
	if got := keys(list); !slices.Equal(got, []alert.Key{cpu, ssh, mem, disk}) {
		t.Fatalf("order %v", got)
	}
	c := list[0]
	if c.RuleName.En != "High CPU usage" || c.Unit != notify.UnitRatio || c.Hostname != "web-01" || c.GroupName != "web" || c.Group != groupA ||
		!c.CanAcknowledge || c.Muted || c.Labels["category"] != "cpu" {
		t.Errorf("cpu %+v", c)
	}
	if m := list[2]; m.AckByName != "Alice" || m.CanAcknowledge {
		t.Errorf("acknowledged %+v", m)
	}
	if d := list[3]; d.CanAcknowledge {
		t.Error("a pending alert cannot be acknowledged")
	}
	// Unknown rules (removed since) show their ID.
	if list[2].RuleName.En != string(mem.Rule) {
		t.Errorf("rule name %+v", list[2].RuleName)
	}
}

// Viewers of a group see its alerts only; fields (user, address) need view_security_events.
func TestList_ScopeAndFields(t *testing.T) {
	f := newFixture(t0)
	list, err := f.svc.List(context.Background(), caller(orgA, in(groupB, role.Viewer), time.Time{}))
	if err != nil || len(list) != 1 || list[0].Key != ssh || list[0].CanAcknowledge {
		t.Fatalf("viewer of B: %+v, %v", list, err)
	}
	if list[0].Fields["user"] != "root" {
		t.Errorf("viewer with logs allowed sees no fields: %+v", list[0].Fields)
	}
	// An organization that hides logs from viewers hides the fields as well.
	closed := newFixtureWith(authz.NewPolicy(authz.WithViewerLogsDisabled(orgA)), t0)
	if list, _ = closed.svc.List(context.Background(), caller(orgA, in(groupB, role.Viewer), time.Time{})); len(list) != 1 || list[0].Fields != nil {
		t.Errorf("fields without view_security_events: %+v", list)
	}
	if list, _ = f.svc.List(context.Background(), caller(orgB, all(role.Admin), time.Time{})); len(list) != 0 {
		t.Errorf("another organization sees %+v", list)
	}
}

func TestList_ShowsWhatIsMuted(t *testing.T) {
	sil := routing.Silence{Match: routing.Matchers{Groups: []tenant.GroupID{groupB}}, StartsAt: t0, EndsAt: t0.Add(time.Hour), Comment: "c"}
	list, err := newFixture(t0.Add(time.Minute), sil).svc.List(context.Background(), caller(orgA, all(role.Viewer), time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Muted != (a.Key == ssh) {
			t.Errorf("%s muted %v", a.Key.Rule, a.Muted)
		}
	}
}

func TestHistory_NewestFirstWithNames(t *testing.T) {
	f := newFixture(t0)
	f.store.history = []alerting.Record{
		{Key: ssh, Transition: alert.Transition{Kind: alert.Acknowledged, From: alert.StateWarning, To: alert.StateWarning, At: t0.Add(time.Minute), By: alice}},
		{Key: ssh, Transition: alert.Transition{Kind: alert.StateChanged, From: alert.StateOK, To: alert.StateWarning, At: t0, Value: 12, Fields: alert.Fields{"user": "root"}}},
	}
	h, err := f.svc.History(context.Background(), caller(orgA, in(groupB, role.Viewer), time.Time{}), ssh)
	if err != nil || len(h) != 2 || h[0].ByName != "Alice" || h[1].Transition.Fields["user"] != "root" {
		t.Fatalf("history %+v, %v", h, err)
	}
	if _, err := f.svc.History(context.Background(), caller(orgA, in(groupA, role.Viewer), time.Time{}), ssh); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("history of a node outside the scope: %v", err)
	}
}

func TestAcknowledge_RoutesTheChange(t *testing.T) {
	f := newFixture(t0.Add(5 * time.Minute))
	if err := f.svc.Acknowledge(context.Background(), caller(orgA, in(groupA, role.Operator), time.Time{}), cpu); err != nil {
		t.Fatal(err)
	}
	if len(f.store.acks) != 1 || f.store.acks[0].Transition.Kind != alert.Acknowledged || f.store.acks[0].Transition.By != alice {
		t.Errorf("acks %+v", f.store.acks)
	}
	if len(f.sink.rounds) != 1 {
		t.Fatalf("rounds %+v", f.sink.rounds)
	}
	r := f.sink.rounds[0]
	acked := slices.IndexFunc(r.Instances, func(i alert.Instance) bool { return i.Key == cpu && i.Ack != nil })
	if r.Org != orgA || !r.Partial || len(r.Records) != 1 || acked < 0 || len(r.Nodes) != 2 || !r.Now.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("round %+v", r)
	}
	if err := f.svc.Acknowledge(context.Background(), caller(orgA, in(groupA, role.Operator), time.Time{}), cpu); !errors.Is(err, alerts.ErrNotFiring) {
		t.Errorf("second acknowledgement: %v", err)
	}
	if err := f.svc.Acknowledge(context.Background(), caller(orgA, in(groupA, role.Operator), time.Time{}), disk); !errors.Is(err, alerts.ErrNotFiring) {
		t.Errorf("pending alert: %v", err)
	}
}

// Operators of the node's group acknowledge; security alerts need a fresh step-up, because
// acknowledging stops their reminders and escalation.
func TestAcknowledge_RightsAndStepUp(t *testing.T) {
	f := newFixture(t0)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		c   access.Caller
		key alert.Key
		err error
	}{
		"viewer":                {caller(orgA, in(groupA, role.Viewer), t0), cpu, access.ErrForbidden},
		"operator of another":   {caller(orgA, in(groupB, role.Operator), t0), cpu, access.ErrNotFound},
		"security without step": {caller(orgA, in(groupB, role.Operator), t0.Add(-time.Hour)), ssh, access.ErrStepUpRequired},
		"another organization":  {caller(orgB, all(role.Admin), t0), cpu, access.ErrNotFound},
		"unknown alert":         {caller(orgA, all(role.Admin), t0), alert.Key{Rule: "system.cpu.steal_high", Node: web}, access.ErrNotFound},
		"security with step":    {caller(orgA, in(groupB, role.Operator), t0), ssh, nil},
	} {
		err := f.svc.Acknowledge(ctx, tc.c, tc.key)
		if tc.err == nil && err != nil || tc.err != nil && !errors.Is(err, tc.err) {
			t.Errorf("%s: %v, want %v", name, err, tc.err)
		}
	}
}
