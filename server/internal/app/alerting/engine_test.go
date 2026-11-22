package alerting_test

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	t0    = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	group = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	web   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
)

// memStore is the alert store in memory; leases behave like the SQL compare-and-swap.
type memStore struct {
	mu        sync.Mutex
	orgs      []tenant.OrgID
	leases    map[tenant.OrgID]lease
	instances map[tenant.OrgID]map[alert.Key]alert.Instance
	history   map[tenant.OrgID][]alerting.Record
	saves     int
}

type lease struct {
	holder string
	until  time.Time
}

func newMemStore(orgs ...tenant.OrgID) *memStore {
	return &memStore{
		orgs: orgs, leases: map[tenant.OrgID]lease{},
		instances: map[tenant.OrgID]map[alert.Key]alert.Instance{}, history: map[tenant.OrgID][]alerting.Record{},
	}
}

func (m *memStore) Organizations(context.Context) ([]tenant.OrgID, error) { return m.orgs, nil }

func (m *memStore) Lease(_ context.Context, org tenant.OrgID, holder string, now, until time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.leases[org]; ok && l.holder != holder && now.Before(l.until) {
		return false, nil
	}
	m.leases[org] = lease{holder: holder, until: until}
	return true, nil
}

func (m *memStore) Instances(_ context.Context, scope tenant.Scope) ([]alert.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []alert.Instance
	for _, i := range m.instances[scope.OrgID()] {
		out = append(out, i)
	}
	return out, nil
}

func (m *memStore) Save(_ context.Context, scope tenant.Scope, changed []alert.Instance, removed []alert.Key, history []alerting.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	org := scope.OrgID()
	if m.instances[org] == nil {
		m.instances[org] = map[alert.Key]alert.Instance{}
	}
	for _, i := range changed {
		m.instances[org][i.Key] = i
	}
	for _, k := range removed {
		delete(m.instances[org], k)
	}
	m.history[org] = append(m.history[org], history...)
	m.saves++
	return nil
}

func (m *memStore) instance(org tenant.OrgID, k alert.Key) (alert.Instance, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.instances[org][k]
	return i, ok
}

type directory struct{}

func (directory) List(_ context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	if scope.OrgID() != orgA {
		return nil, nil
	}
	return []fleet.Node{{ID: web, Group: group, Hostname: "web-01"}}, nil
}

func (directory) HasGroup(context.Context, tenant.Scope, tenant.GroupID) (bool, error) {
	return true, nil
}

// valueRule reports the current value of a fake metric for every node; NaN means no data.
type valueRule struct {
	mu     sync.Mutex
	value  float64
	err    error
	scopes []tenant.Scope
}

var cpu = alert.Thresholds{Warning: &alert.Condition{Op: alert.Above, Value: 0.85}, Clear: &alert.Condition{Op: alert.Below, Value: 0.80}}

func (v *valueRule) set(x float64) { v.mu.Lock(); v.value = x; v.mu.Unlock() }

func (v *valueRule) Evaluate(_ context.Context, env alerting.Env) ([]alerting.Result, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.scopes = append(v.scopes, env.Scope)
	if v.err != nil {
		return nil, v.err
	}
	out := make([]alerting.Result, 0, len(env.Nodes))
	for _, n := range env.Nodes {
		out = append(out, alerting.Result{Node: n.ID, Observation: cpu.Evaluate(v.value)})
	}
	return out, nil
}

type rules map[tenant.OrgID][]alerting.Rule

func (r rules) Rules(_ context.Context, org tenant.OrgID) ([]alerting.Rule, error) {
	return r[org], nil
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

const cpuRule alert.RuleID = "system.cpu.usage_high"

var cpuKey = alert.Key{Rule: cpuRule, Node: web}

func engine(store *memStore, source alerting.RuleSource, c *clock, holder string) *alerting.Engine {
	return alerting.NewEngine(alerting.Deps{
		Store: store, Rules: source, Nodes: directory{}, Now: c.Now, Holder: holder,
		Logger: slog.New(slog.DiscardHandler),
	})
}

func cpuRules(v *valueRule, forDuration time.Duration) rules {
	return rules{orgA: {{ID: cpuRule, Timing: alert.Timing{For: forDuration, Flapping: alert.DefaultFlapping}, Evaluator: v}}}
}

// rounds runs n rounds, advancing the clock by the interval before each.
func rounds(t *testing.T, e *alerting.Engine, c *clock, n int) {
	t.Helper()
	for range n {
		c.advance(alerting.Interval)
		e.Round(context.Background())
	}
}

func TestEngine_FiresAfterForAndRecordsTheHistory(t *testing.T) {
	store, v, c := newMemStore(orgA), &valueRule{value: 0.90}, &clock{now: t0}
	e := engine(store, cpuRules(v, time.Minute), c, "a")

	rounds(t, e, c, 1)
	if i, _ := store.instance(orgA, cpuKey); i.State != alert.StatePending {
		t.Fatalf("after one round: %v, want pending", i.State)
	}
	rounds(t, e, c, 6)
	i, _ := store.instance(orgA, cpuKey)
	if i.State != alert.StateWarning || i.Value != 0.90 {
		t.Fatalf("after a minute: %+v, want warning", i)
	}
	h := store.history[orgA]
	if len(h) != 2 || h[0].Transition.To != alert.StatePending || h[1].Transition.To != alert.StateWarning || h[1].Key != cpuKey {
		t.Errorf("history = %+v, want pending then warning", h)
	}
	// Only rounds with changes write.
	if store.saves != 2 {
		t.Errorf("%d saves for 7 rounds with 2 changes, want 2", store.saves)
	}
}

// Back to OK without flapping history the instance disappears (the table holds what matters);
// its history stays.
func TestEngine_RemovesInstancesThatAreQuietAgain(t *testing.T) {
	store, v, c := newMemStore(orgA), &valueRule{value: 0.90}, &clock{now: t0}
	e := engine(store, cpuRules(v, 0), c, "a")
	rounds(t, e, c, 1)
	v.set(0.10)
	rounds(t, e, c, 2) // resolved, then OK
	if i, ok := store.instance(orgA, cpuKey); !ok || len(i.Flips) == 0 {
		t.Fatalf("an instance with recent flips is kept for the flapping detection: %+v, %v", i, ok)
	}
	c.advance(alert.DefaultFlapping.Window)
	rounds(t, e, c, 1)
	if _, ok := store.instance(orgA, cpuKey); ok {
		t.Error("a quiet instance must be removed")
	}
	if len(store.history[orgA]) != 3 {
		t.Errorf("history = %d entries, want firing, resolved, ok", len(store.history[orgA]))
	}
}

func TestEngine_MissingDataRaisesNoAlert(t *testing.T) {
	store, v, c := newMemStore(orgA), &valueRule{value: math.NaN()}, &clock{now: t0}
	e := engine(store, cpuRules(v, 0), c, "a")
	rounds(t, e, c, 5)
	if _, ok := store.instance(orgA, cpuKey); ok || store.saves != 0 {
		t.Errorf("no data created an instance or wrote (saves %d)", store.saves)
	}
}

// After a restart (or a takeover by another instance) a pending alert fires at its original
// deadline, not one "for" after the restart.
func TestEngine_ContinuesAPendingAlertAfterARestart(t *testing.T) {
	store, v, c := newMemStore(orgA), &valueRule{value: 0.90}, &clock{now: t0}
	rounds(t, engine(store, cpuRules(v, time.Minute), c, "a"), c, 3) // pending for 20 s

	restarted := engine(store, cpuRules(v, time.Minute), c, "a")
	rounds(t, restarted, c, 3)
	if i, _ := store.instance(orgA, cpuKey); i.State != alert.StatePending {
		t.Fatalf("50 s pending: %v", i.State)
	}
	rounds(t, restarted, c, 1)
	if i, _ := store.instance(orgA, cpuKey); i.State != alert.StateWarning {
		t.Errorf("60 s after the first excursion: %v, want warning", i.State)
	}
}

// Several server instances: one evaluates an organization, the other takes over once the lease
// of a stopped one expires.
func TestEngine_OnlyTheLeaseHolderEvaluates(t *testing.T) {
	store, c := newMemStore(orgA), &clock{now: t0}
	va, vb := &valueRule{value: 0.5}, &valueRule{value: 0.5}
	a, b := engine(store, cpuRules(va, 0), c, "a"), engine(store, cpuRules(vb, 0), c, "b")
	for range 5 {
		c.advance(alerting.Interval)
		a.Round(context.Background())
		b.Round(context.Background())
	}
	if len(va.scopes) != 5 || len(vb.scopes) != 0 {
		t.Fatalf("evaluations a=%d b=%d, want only the holder", len(va.scopes), len(vb.scopes))
	}
	// a stops; b takes over after the lease ran out.
	c.advance(alerting.LeaseDuration)
	b.Round(context.Background())
	if len(vb.scopes) != 1 {
		t.Errorf("b after the lease expired: %d evaluations, want 1", len(vb.scopes))
	}
}

// Each organization is evaluated in its own scope; one failing rule doesn't stop the others.
func TestEngine_EvaluatesEachOrganizationInItsScope(t *testing.T) {
	store, c := newMemStore(orgA, orgB), &clock{now: t0}
	broken, good := &valueRule{err: errors.New("metrics unavailable")}, &valueRule{value: 0.9}
	source := rules{
		orgA: {
			{ID: "system.cpu.broken_rule", Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: broken},
			{ID: cpuRule, Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: good},
		},
		orgB: {{ID: cpuRule, Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: good}},
	}
	e := engine(store, source, c, "a")
	rounds(t, e, c, 1)

	if i, _ := store.instance(orgA, cpuKey); i.State != alert.StateWarning {
		t.Errorf("the good rule of org A: %v, want warning despite the broken one", i.State)
	}
	if _, ok := store.instance(orgB, cpuKey); ok {
		t.Error("org B has no such node: nothing may appear there")
	}
	for _, s := range good.scopes {
		if !s.AllGroups() || (s.OrgID() != orgA && s.OrgID() != orgB) {
			t.Errorf("evaluated with scope %+v", s)
		}
	}
	if e.Stats().RuleErrors.Load() != 1 {
		t.Errorf("rule errors = %d, want 1", e.Stats().RuleErrors.Load())
	}
}

// A rule that no longer applies – disabled, removed, or the node left its groups – ends its
// alerts: a firing one resolves (history and notification), and the instance goes.
func TestEngine_RetiresAlertsOfRulesThatNoLongerApply(t *testing.T) {
	other := tenant.GroupID(uuid.New())
	for name, next := range map[string][]alerting.Rule{
		"rule disabled":       nil,
		"node outside groups": {{ID: cpuRule, Groups: []tenant.GroupID{other}, Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: &valueRule{value: 0.9}}},
		// M2-29: the node lacks the rule's tags (they were removed, or the rule now asks for them).
		"node without the tags": {{ID: cpuRule, Tags: tag.Tags{"env": "prod"}, Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: &valueRule{value: 0.9}}},
	} {
		store, c := newMemStore(orgA), &clock{now: t0}
		source := cpuRules(&valueRule{value: 0.90}, 0)
		e := engine(store, source, c, "a")
		rounds(t, e, c, 1)
		if i, _ := store.instance(orgA, cpuKey); i.State != alert.StateWarning {
			t.Fatalf("%s: %v, want warning", name, i.State)
		}
		source[orgA] = next
		rounds(t, e, c, 1)
		if i, ok := store.instance(orgA, cpuKey); ok {
			t.Errorf("%s: instance kept: %+v", name, i)
		}
		h := store.history[orgA]
		if last := h[len(h)-1].Transition; last.From != alert.StateWarning || last.To != alert.StateResolved || !last.At.Equal(c.Now()) {
			t.Errorf("%s: last transition %+v, want warning → resolved now", name, last)
		}
	}
}

// A rule whose evaluation fails keeps its alerts: an error is no sign that the problem is gone.
func TestEngine_KeepsAlertsOfAFailingRule(t *testing.T) {
	store, v, c := newMemStore(orgA), &valueRule{value: 0.90}, &clock{now: t0}
	e := engine(store, cpuRules(v, 0), c, "a")
	rounds(t, e, c, 1)
	v.mu.Lock()
	v.err = errors.New("query failed")
	v.mu.Unlock()
	rounds(t, e, c, 2)
	if i, _ := store.instance(orgA, cpuKey); i.State != alert.StateWarning {
		t.Errorf("state = %v, want warning", i.State)
	}
}

type sink struct {
	mu     sync.Mutex
	rounds []alerting.Round
}

func (s *sink) Deliver(_ context.Context, r alerting.Round) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rounds = append(s.rounds, r)
}

// The routing hears of every round the engine holds, also without changes: repeats and
// escalations need the firing instances.
func TestEngine_HandsEveryRoundToTheSink(t *testing.T) {
	store, v, c, s := newMemStore(orgA, orgB), &valueRule{value: 0.90}, &clock{now: t0}, &sink{}
	e := alerting.NewEngine(alerting.Deps{
		Store: store, Rules: cpuRules(v, 0), Nodes: directory{}, Now: c.Now, Holder: "a", Sink: s,
		Logger: slog.New(slog.DiscardHandler),
	})
	rounds(t, e, c, 2)
	if len(s.rounds) != 4 {
		t.Fatalf("%d rounds for 2 rounds of 2 organizations", len(s.rounds))
	}
	first, second := s.rounds[0], s.rounds[2]
	if first.Org != orgA || len(first.Records) != 1 || first.Records[0].Transition.To != alert.StateWarning ||
		len(first.Instances) != 1 || first.Instances[0].State != alert.StateWarning || len(first.Nodes) != 1 || !first.Now.Equal(t0.Add(alerting.Interval)) {
		t.Errorf("first round %+v", first)
	}
	if second.Org != orgA || len(second.Records) != 0 || len(second.Instances) != 1 || second.Instances[0].Key != cpuKey {
		t.Errorf("second round %+v", second)
	}
	if s.rounds[1].Org != orgB || len(s.rounds[1].Instances) != 0 {
		t.Errorf("organization B %+v", s.rounds[1])
	}
	// Removed instances are not handed on.
	v.set(0.10)
	rounds(t, e, c, 2) // resolved, then OK (kept for flapping), then gone
	c.advance(alert.DefaultFlapping.Window)
	rounds(t, e, c, 1)
	if last := s.rounds[len(s.rounds)-2]; last.Org != orgA || len(last.Instances) != 0 {
		t.Errorf("after removal %+v", last)
	}
	// Another holder's organization is not handed on.
	other := alerting.NewEngine(alerting.Deps{Store: store, Rules: cpuRules(v, 0), Nodes: directory{}, Now: c.Now, Holder: "b", Sink: s, Logger: slog.New(slog.DiscardHandler)})
	before := len(s.rounds)
	other.Round(context.Background())
	if len(s.rounds) != before {
		t.Errorf("a round without the lease reached the sink")
	}
}
