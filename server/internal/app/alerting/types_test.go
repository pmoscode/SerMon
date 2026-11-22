package alerting_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var db01 = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))

// fakeMetrics answers queries from fixed series and records them.
type fakeMetrics struct {
	mu      sync.Mutex
	series  map[string][]metrics.Series // by metric
	queries []metrics.Query
}

func (f *fakeMetrics) Query(_ context.Context, scope tenant.Scope, q metrics.Query) ([]metrics.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !scope.Valid() {
		return nil, errors.New("query without scope")
	}
	f.queries = append(f.queries, q)
	var out []metrics.Series
	for _, s := range f.series[q.Metric] {
		if !contains(q.Nodes, s.Node) || !hasLabels(s.Labels, q.Labels) {
			continue
		}
		var points []metrics.Point
		for _, p := range s.Points {
			if !p.Time.Before(q.From) && p.Time.Before(q.To) {
				points = append(points, p)
			}
		}
		out = append(out, metrics.Series{Node: s.Node, Labels: s.Labels, Points: points})
	}
	return out, nil
}

func contains(nodes []identity.NodeID, n identity.NodeID) bool {
	for _, x := range nodes {
		if x == n {
			return true
		}
	}
	return false
}

func hasLabels(have, want metrics.Labels) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// points returns one point per step ending just before now, with the given values.
func points(step time.Duration, values ...float64) []metrics.Point {
	out := make([]metrics.Point, len(values))
	for i, v := range values {
		out[i] = metrics.Point{Time: t0.Add(-time.Duration(len(values)-i) * step), Avg: v, Min: v, Max: v}
	}
	return out
}

func env(m *fakeMetrics, nodes ...fleet.Node) alerting.Env {
	scope, _ := tenant.ForAllGroups(orgA)
	return alerting.Env{Scope: scope, Nodes: nodes, Now: t0, Metrics: m}
}

var (
	webNode = fleet.Node{ID: web, Group: group, Hostname: "web-01"}
	dbNode  = fleet.Node{ID: db01, Group: group, Hostname: "db-01"}
)

func byKey(t *testing.T, results []alerting.Result) map[string]alert.Observation {
	t.Helper()
	out := map[string]alert.Observation{}
	for _, r := range results {
		k := r.Node.String() + "|" + r.Dimension
		if _, dup := out[k]; dup {
			t.Fatalf("two results for %s", k)
		}
		out[k] = r.Observation
	}
	return out
}

func gt(v float64) *alert.Condition { return &alert.Condition{Op: alert.Above, Value: v} }
func lt(v float64) *alert.Condition { return &alert.Condition{Op: alert.Below, Value: v} }

// CPU busy is idle < 15 %: ratios arrive in ppm and are compared as 0..1.
func TestThreshold_AveragesTheWindowInRatios(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{"sermon_cpu_utilization_ratio": {
		{Node: web, Labels: metrics.Labels{"mode": "idle"}, Points: points(time.Minute, 100_000, 100_000, 200_000)},
		{Node: web, Labels: metrics.Labels{"mode": "user"}, Points: points(time.Minute, 900_000, 900_000, 800_000)},
		{Node: db01, Labels: metrics.Labels{"mode": "idle"}, Points: points(time.Minute, 900_000)},
	}}}
	rule := alerting.ThresholdRule{
		Series:    alerting.SeriesSpec{Metric: "sermon_cpu_utilization_ratio", Labels: metrics.Labels{"mode": "idle"}},
		Aggregate: alerting.Avg, Window: 10 * time.Minute,
		Thresholds: alert.Thresholds{Warning: lt(0.15), Critical: lt(0.05), Clear: gt(0.20)},
	}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	results, err := rule.Evaluate(context.Background(), env(m, webNode, dbNode))
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(t, results)
	if o := got[web.String()+"|"]; o.Level != alert.LevelWarning || math.Abs(o.Value-0.1333) > 0.001 {
		t.Errorf("web: %+v, want warning at 0.133", o)
	}
	if o := got[db01.String()+"|"]; o.Level != alert.LevelOK || !o.Cleared {
		t.Errorf("db: %+v, want ok", o)
	}
	q := m.queries[0]
	if !q.From.Equal(t0.Add(-10*time.Minute)) || !q.To.Equal(t0) || q.Step != 0 || len(q.Nodes) != 2 {
		t.Errorf("query = %+v, want the window up to now for both nodes", q)
	}
}

func TestThreshold_AggregatesAndDimensions(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{"sermon_filesystem_utilization_ratio": {
		{Node: web, Labels: metrics.Labels{"mountpoint": "/"}, Points: points(time.Minute, 500_000, 950_000, 700_000)},
		{Node: web, Labels: metrics.Labels{"mountpoint": "/var"}, Points: points(time.Minute, 810_000)},
		{Node: web, Labels: metrics.Labels{"mountpoint": "/run"}, Points: points(time.Minute, 990_000)},
	}}}
	base := alerting.ThresholdRule{
		Series: alerting.SeriesSpec{
			Metric: "sermon_filesystem_utilization_ratio", Dimensions: []string{"mountpoint"},
			Exclude: map[string][]string{"mountpoint": {"/run", "/dev/*"}},
		},
		Window:     5 * time.Minute,
		Thresholds: alert.Thresholds{Warning: gt(0.80), Critical: gt(0.90)},
	}
	root := metrics.Labels{"mountpoint": "/"}.Key()
	for agg, want := range map[alerting.Aggregate]float64{alerting.Max: 0.95, alerting.Min: 0.50, alerting.Last: 0.70, alerting.Avg: 0.7166} {
		rule := base
		rule.Aggregate = agg
		results, err := rule.Evaluate(context.Background(), env(m, webNode))
		if err != nil {
			t.Fatal(err)
		}
		got := byKey(t, results)
		if len(got) != 2 {
			t.Fatalf("%v: %d instances, want / and /var (/run excluded)", agg, len(got))
		}
		if o := got[web.String()+"|"+root]; math.Abs(o.Value-want) > 0.001 {
			t.Errorf("%v of /: %v, want %v", agg, o.Value, want)
		}
	}
}

// increase (M2-24): how much a cumulative counter grew within the window; after a reset (agent
// restart) it counts from 0 again. The buffer's dropped batches are such a counter.
func TestThreshold_IncreaseOfACumulativeCounter(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{"sermon_agent_buffer_dropped_batches": {
		{Node: web, Points: points(time.Minute, 3, 3, 5, 9)},
		{Node: db01, Points: points(time.Minute, 7, 9, 2, 4)}, // reset after 9: +2, +2, +2
		{Node: app01, Points: points(time.Minute, 4, 4, 4)},
	}}}
	rule := alerting.ThresholdRule{
		Series: alerting.SeriesSpec{Metric: "sermon_agent_buffer_dropped_batches"}, Aggregate: alerting.Increase,
		Window: 15 * time.Minute, Thresholds: alert.Thresholds{Warning: gt(0)},
	}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	results, err := rule.Evaluate(context.Background(), env(m, webNode, dbNode, appNode))
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(t, results)
	for node, want := range map[identity.NodeID]float64{web: 6, db01: 6, app01: 0} {
		if o := got[node.String()+"|"]; o.Value != want {
			t.Errorf("%v: increase %v, want %v", node, o.Value, want)
		}
	}
	if got[app01.String()+"|"].Level != alert.LevelOK || got[web.String()+"|"].Level != alert.LevelWarning {
		t.Errorf("levels: %+v", got)
	}
	// A ratio of increases means nothing.
	rule.RelativeTo = "sermon_agent_buffer_pending_batches"
	if err := rule.Validate(); !errors.Is(err, alert.ErrInvalidRule) {
		t.Errorf("increase with relative_to: %v", err)
	}
}

func TestThreshold_NodesWithoutDataGetNoResult(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{}}
	rule := alerting.ThresholdRule{
		Series:    alerting.SeriesSpec{Metric: "sermon_load_average_per_cpu", Labels: metrics.Labels{"period": "15m"}},
		Aggregate: alerting.Avg, Window: 10 * time.Minute, Thresholds: alert.Thresholds{Warning: gt(1.5)},
	}
	results, err := rule.Evaluate(context.Background(), env(m, webNode))
	if err != nil || len(results) != 0 {
		t.Errorf("results = %+v, %v; want none (missing data is the absence rule's business)", results, err)
	}
}

// The query layer accepts at most metrics.MaxNodes per query.
func TestThreshold_SplitsLargeFleetsIntoSeveralQueries(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{}}
	nodes := make([]fleet.Node, metrics.MaxNodes+1)
	for i := range nodes {
		nodes[i] = fleet.Node{ID: identity.NodeID(uuid.New()), Group: group}
	}
	rule := alerting.ThresholdRule{
		Series:    alerting.SeriesSpec{Metric: "sermon_load_average_per_cpu", Labels: metrics.Labels{"period": "15m"}},
		Aggregate: alerting.Avg, Window: time.Minute, Thresholds: alert.Thresholds{Warning: gt(1.5)},
	}
	if _, err := rule.Evaluate(context.Background(), env(m, nodes...)); err != nil {
		t.Fatal(err)
	}
	if len(m.queries) != 2 || len(m.queries[0].Nodes) != metrics.MaxNodes || len(m.queries[1].Nodes) != 1 {
		t.Errorf("%d queries, want 2 (%d + 1 nodes)", len(m.queries), metrics.MaxNodes)
	}
}

// Errors relative to packets (> 0.1 %), and overflows per minute.
func TestRate_PerMinuteAndRelative(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{
		"sermon_network_receive_errors_per_second":  {{Node: web, Labels: metrics.Labels{"interface": "eth0"}, Points: points(time.Minute, 2, 2)}},
		"sermon_network_receive_packets_per_second": {{Node: web, Labels: metrics.Labels{"interface": "eth0"}, Points: points(time.Minute, 1000, 1000)}},
		"sermon_tcp_listen_overflows_per_second":    {{Node: web, Points: points(time.Minute, 0.5, 0)}},
	}}
	relative := alerting.RateRule{
		Series:     alerting.SeriesSpec{Metric: "sermon_network_receive_errors_per_second", Dimensions: []string{"interface"}},
		RelativeTo: "sermon_network_receive_packets_per_second", Window: 5 * time.Minute,
		Thresholds: alert.Thresholds{Warning: gt(0.001), Critical: gt(0.01)},
	}
	if err := relative.Validate(); err != nil {
		t.Fatal(err)
	}
	results, err := relative.Evaluate(context.Background(), env(m, webNode))
	if err != nil || len(results) != 1 || results[0].Observation.Level != alert.LevelWarning || math.Abs(results[0].Observation.Value-0.002) > 1e-9 {
		t.Fatalf("relative rate = %+v, %v; want warning at 0.2 %%", results, err)
	}
	perMinute := alerting.RateRule{
		Series: alerting.SeriesSpec{Metric: "sermon_tcp_listen_overflows_per_second"}, Per: time.Minute, Window: 5 * time.Minute,
		Thresholds: alert.Thresholds{Warning: gt(0), Critical: gt(10)},
	}
	results, err = perMinute.Evaluate(context.Background(), env(m, webNode))
	if err != nil || len(results) != 1 || results[0].Observation.Value != 15 || results[0].Observation.Level != alert.LevelCritical {
		t.Errorf("per minute = %+v, %v; want 15/min critical", results, err)
	}
}

func TestRate_NoTrafficIsNoProblem(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{
		"sermon_network_receive_errors_per_second":  {{Node: web, Labels: metrics.Labels{"interface": "eth1"}, Points: points(time.Minute, 0)}},
		"sermon_network_receive_packets_per_second": {{Node: web, Labels: metrics.Labels{"interface": "eth1"}, Points: points(time.Minute, 0)}},
	}}
	rule := alerting.RateRule{
		Series:     alerting.SeriesSpec{Metric: "sermon_network_receive_errors_per_second", Dimensions: []string{"interface"}},
		RelativeTo: "sermon_network_receive_packets_per_second", Window: 5 * time.Minute,
		Thresholds: alert.Thresholds{Warning: gt(0.001)},
	}
	results, _ := rule.Evaluate(context.Background(), env(m, webNode))
	if len(results) != 1 || results[0].Observation.Value != 0 || results[0].Observation.Level != alert.LevelOK {
		t.Errorf("idle interface = %+v, want 0", results)
	}
}

func TestAbsence_MeasuresTheTimeSinceTheLastContact(t *testing.T) {
	seen := func(ago time.Duration) *time.Time { at := t0.Add(-ago); return &at }
	rule := alerting.AbsenceRule{Thresholds: alert.Thresholds{Critical: gt(60)}}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	fresh := fleet.Node{ID: web, LastSeenAt: seen(30 * time.Second)}
	gone := fleet.Node{ID: db01, LastSeenAt: seen(90 * time.Second)}
	never := fleet.Node{ID: identity.NodeID(uuid.New())}
	results, err := rule.Evaluate(context.Background(), env(&fakeMetrics{}, fresh, gone, never))
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(t, results)
	if o := got[web.String()+"|"]; o.Level != alert.LevelOK || o.Value != 30 {
		t.Errorf("seen 30 s ago: %+v", o)
	}
	if o := got[db01.String()+"|"]; o.Level != alert.LevelCritical || o.Value != 90 {
		t.Errorf("seen 90 s ago: %+v, want critical", o)
	}
	if _, ok := got[never.ID.String()+"|"]; ok {
		t.Error("a node never seen is no outage (it has not started yet)")
	}
}

// A read-only file system is critical; an interface counts as down only if it was up before
// (unused ports must not alert).
func TestState_LastValueAndWhatWasSeenBefore(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{
		"sermon_filesystem_readonly": {{Node: web, Labels: metrics.Labels{"mountpoint": "/data"}, Points: points(time.Minute, 0, 1)}},
		"sermon_network_up": {
			{Node: web, Labels: metrics.Labels{"interface": "eth0"}, Points: append(points(time.Hour, 1, 1), metrics.Point{Time: t0.Add(-time.Minute)})},
			{Node: web, Labels: metrics.Labels{"interface": "eth9"}, Points: append(points(time.Hour, 0, 0), metrics.Point{Time: t0.Add(-time.Minute)})},
		},
	}}
	readonly := alerting.StateRule{
		Series:     alerting.SeriesSpec{Metric: "sermon_filesystem_readonly", Dimensions: []string{"mountpoint"}},
		Thresholds: alert.Thresholds{Critical: &alert.Condition{Op: alert.AboveOrEqual, Value: 1}},
	}
	results, err := readonly.Evaluate(context.Background(), env(m, webNode))
	if err != nil || len(results) != 1 || results[0].Observation.Level != alert.LevelCritical {
		t.Fatalf("read-only = %+v, %v", results, err)
	}

	down := alerting.StateRule{
		Series:     alerting.SeriesSpec{Metric: "sermon_network_up", Dimensions: []string{"interface"}},
		Thresholds: alert.Thresholds{Critical: lt(1)},
		Seen:       &alert.Condition{Op: alert.AboveOrEqual, Value: 1}, SeenWindow: 24 * time.Hour,
	}
	if err := down.Validate(); err != nil {
		t.Fatal(err)
	}
	results, err = down.Evaluate(context.Background(), env(m, webNode))
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(t, results)
	if o := got[web.String()+"|"+metrics.Labels{"interface": "eth0"}.Key()]; o.Level != alert.LevelCritical {
		t.Errorf("eth0 went down: %+v, want critical", o)
	}
	if o, ok := got[web.String()+"|"+metrics.Labels{"interface": "eth9"}.Key()]; ok && o.Level != alert.LevelOK {
		t.Errorf("eth9 was never up: %+v, want no alert", o)
	}
}

// Disk full in < 24 h / < 6 h: hours until the available space reaches zero.
func TestPrediction_HoursUntilTheMetricReachesZero(t *testing.T) {
	falling := make([]float64, 13) // hourly points, 10 GB less per hour, 50 GB left an hour ago
	for i := range falling {
		falling[i] = 170e9 - float64(i)*10e9
	}
	m := &fakeMetrics{series: map[string][]metrics.Series{"sermon_filesystem_avail_bytes": {
		{Node: web, Labels: metrics.Labels{"mountpoint": "/"}, Points: points(time.Hour, falling...)},
		{Node: web, Labels: metrics.Labels{"mountpoint": "/var"}, Points: points(time.Hour, 1e9, 2e9, 3e9, 4e9, 5e9, 6e9, 7e9)},
		{Node: web, Labels: metrics.Labels{"mountpoint": "/tmp"}, Points: points(time.Minute, 5e9, 4e9)},
	}}}
	rule := alerting.PredictionRule{
		Series:   alerting.SeriesSpec{Metric: "sermon_filesystem_avail_bytes", Dimensions: []string{"mountpoint"}},
		Lookback: 24 * time.Hour, Thresholds: alert.Thresholds{Warning: lt(24), Critical: lt(6)},
	}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	results, err := rule.Evaluate(context.Background(), env(m, webNode))
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(t, results)
	if o := got[web.String()+"|"+metrics.Labels{"mountpoint": "/"}.Key()]; o.Level != alert.LevelCritical || math.Abs(o.Value-4) > 0.1 {
		t.Errorf("/ full in 4 h: %+v, want critical", o)
	}
	if o := got[web.String()+"|"+metrics.Labels{"mountpoint": "/var"}.Key()]; o.Level != alert.LevelOK || !o.Cleared {
		t.Errorf("/var is growing free space: %+v, want ok", o)
	}
	if o, ok := got[web.String()+"|"+metrics.Labels{"mountpoint": "/tmp"}.Key()]; ok && !o.NoData {
		t.Errorf("/tmp has too little history for a trend: %+v, want no data", o)
	}
}

func TestRules_ValidateAgainstTheCatalog(t *testing.T) {
	th := alert.Thresholds{Warning: gt(1)}
	for name, rule := range map[string]interface{ Validate() error }{
		"unknown metric":    alerting.ThresholdRule{Series: alerting.SeriesSpec{Metric: "sermon_nope"}, Aggregate: alerting.Avg, Window: time.Minute, Thresholds: th},
		"unknown label":     alerting.ThresholdRule{Series: alerting.SeriesSpec{Metric: "sermon_cpu_utilization_ratio", Labels: metrics.Labels{"core": "1"}}, Aggregate: alerting.Avg, Window: time.Minute, Thresholds: th},
		"label not covered": alerting.ThresholdRule{Series: alerting.SeriesSpec{Metric: "sermon_cpu_utilization_ratio"}, Aggregate: alerting.Avg, Window: time.Minute, Thresholds: th},
		"no window":         alerting.ThresholdRule{Series: alerting.SeriesSpec{Metric: "sermon_system_zombies"}, Aggregate: alerting.Avg, Thresholds: th},
		"window too long":   alerting.ThresholdRule{Series: alerting.SeriesSpec{Metric: "sermon_system_zombies"}, Aggregate: alerting.Avg, Window: 8 * 24 * time.Hour, Thresholds: th},
		"no aggregate":      alerting.ThresholdRule{Series: alerting.SeriesSpec{Metric: "sermon_system_zombies"}, Window: time.Minute, Thresholds: th},
		"bad thresholds":    alerting.ThresholdRule{Series: alerting.SeriesSpec{Metric: "sermon_system_zombies"}, Aggregate: alerting.Avg, Window: time.Minute},
		"rate of a ratio":   alerting.RateRule{Series: alerting.SeriesSpec{Metric: "sermon_cpu_core_busy_ratio", Dimensions: []string{"cpu"}}, Window: time.Minute, Thresholds: th},
		"relative unknown":  alerting.RateRule{Series: alerting.SeriesSpec{Metric: "sermon_tcp_listen_overflows_per_second"}, RelativeTo: "sermon_nope", Window: time.Minute, Thresholds: th},
		"per of a relative": alerting.RateRule{Series: alerting.SeriesSpec{Metric: "sermon_network_receive_errors_per_second", Dimensions: []string{"interface"}}, RelativeTo: "sermon_network_receive_packets_per_second", Per: time.Minute, Window: time.Minute, Thresholds: th},
		"odd per":           alerting.RateRule{Series: alerting.SeriesSpec{Metric: "sermon_tcp_listen_overflows_per_second"}, Per: 7 * time.Second, Window: time.Minute, Thresholds: th},
		"seen w/o window":   alerting.StateRule{Series: alerting.SeriesSpec{Metric: "sermon_network_up", Dimensions: []string{"interface"}}, Thresholds: th, Seen: gt(0)},
		"short lookback":    alerting.PredictionRule{Series: alerting.SeriesSpec{Metric: "sermon_filesystem_avail_bytes", Dimensions: []string{"mountpoint"}}, Lookback: time.Minute, Thresholds: th},
		"absence w/o level": alerting.AbsenceRule{},
	} {
		err := rule.Validate()
		if !errors.Is(err, alert.ErrInvalidRule) && !errors.Is(err, alert.ErrInvalidThresholds) {
			t.Errorf("%s: Validate() = %v, want an invalid rule", name, err)
		} else if err != nil && strings.Contains(err.Error(), "%!") {
			t.Errorf("%s: malformed message %q", name, err)
		}
	}
}

// A rule for selected groups only sees their nodes.
func TestEngine_LimitsRulesToTheirGroups(t *testing.T) {
	store, c := newMemStore(orgA), &clock{now: t0}
	v := &valueRule{value: 0.9}
	other := tenant.GroupID(uuid.New())
	source := rules{orgA: {{ID: cpuRule, Groups: []tenant.GroupID{other}, Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: v}}}
	rounds(t, engine(store, source, c, "a"), c, 1)
	if _, ok := store.instance(orgA, cpuKey); ok {
		t.Error("a rule for another group evaluated web-01")
	}
}

// A state without a current value (agent stopped sending it) yields nothing instead of the last
// known state.
func TestState_IgnoresStaleValues(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{
		"sermon_filesystem_readonly": {{Node: web, Labels: metrics.Labels{"mountpoint": "/data"}, Points: points(time.Hour, 1)}},
	}}
	rule := alerting.StateRule{
		Series:     alerting.SeriesSpec{Metric: "sermon_filesystem_readonly", Dimensions: []string{"mountpoint"}},
		Thresholds: alert.Thresholds{Critical: &alert.Condition{Op: alert.AboveOrEqual, Value: 1}},
	}
	if results, _ := rule.Evaluate(context.Background(), env(m, webNode)); len(results) != 0 {
		t.Errorf("results = %+v, want none for a value an hour old", results)
	}
}

// Against the real reader: a 10-minute window comes from the ring buffer with 1-s values.
func TestThreshold_ReadsLiveValuesFromTheRing(t *testing.T) {
	ring := metrics.NewRing()
	for s := range 600 {
		ring.Add(orgA, group, web, "sermon_cpu_utilization_ratio", metrics.Labels{"mode": "idle"}, t0.Add(-time.Duration(600-s)*time.Second), 50_000)
	}
	store := memstore.New()
	store.AddNode(orgA, group, web)
	reader := metrics.NewReader(store, ring, func() time.Time { return t0 })
	rule := alerting.ThresholdRule{
		Series:    alerting.SeriesSpec{Metric: "sermon_cpu_utilization_ratio", Labels: metrics.Labels{"mode": "idle"}},
		Aggregate: alerting.Avg, Window: 10 * time.Minute, Thresholds: alert.Thresholds{Warning: lt(0.15), Critical: lt(0.10)},
	}
	scope, _ := tenant.ForAllGroups(orgA)
	results, err := rule.Evaluate(context.Background(), alerting.Env{Scope: scope, Nodes: []fleet.Node{webNode}, Now: t0, Metrics: reader})
	if err != nil || len(results) != 1 || results[0].Observation.Level != alert.LevelCritical || math.Abs(results[0].Observation.Value-0.05) > 1e-9 {
		t.Errorf("results = %+v, %v; want critical at 5 %% idle", results, err)
	}
}

// Inodes and file descriptors have no ratio metric: used relative to total.
func TestThreshold_RelativeToASecondMetric(t *testing.T) {
	m := &fakeMetrics{series: map[string][]metrics.Series{
		"sermon_filesystem_inodes_used":  {{Node: web, Labels: metrics.Labels{"mountpoint": "/"}, Points: points(time.Minute, 850)}},
		"sermon_filesystem_inodes_total": {{Node: web, Labels: metrics.Labels{"mountpoint": "/"}, Points: points(time.Minute, 1000)}},
	}}
	rule := alerting.ThresholdRule{
		Series:     alerting.SeriesSpec{Metric: "sermon_filesystem_inodes_used", Dimensions: []string{"mountpoint"}},
		RelativeTo: "sermon_filesystem_inodes_total", Aggregate: alerting.Last, Window: 5 * time.Minute,
		Thresholds: alert.Thresholds{Warning: gt(0.80), Critical: gt(0.90)},
	}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	results, err := rule.Evaluate(context.Background(), env(m, webNode))
	if err != nil || len(results) != 1 || results[0].Observation.Level != alert.LevelWarning || results[0].Observation.Value != 0.85 {
		t.Errorf("results = %+v, %v; want warning at 0.85", results, err)
	}
	bad := rule
	bad.RelativeTo = "sermon_memory_total_bytes" // other labels
	if err := bad.Validate(); !errors.Is(err, alert.ErrInvalidRule) {
		t.Errorf("relative to a metric with other labels: %v", err)
	}
}

// Rules run every 10 s: long lookbacks read the 1-min aggregate with a coarse step instead of the
// raw table, and a state's current value still comes from the last minutes (ring buffer).
func TestQueries_LongWindowsUseCoarseSteps(t *testing.T) {
	spec := alerting.SeriesSpec{Metric: "sermon_network_up", Dimensions: []string{"interface"}}
	for name, tc := range map[string]struct {
		rule  alerting.Evaluator
		steps map[time.Duration]time.Duration // span → step
	}{
		"short threshold": {alerting.ThresholdRule{Series: spec, Aggregate: alerting.Last, Window: 10 * time.Minute, Thresholds: alert.Thresholds{Warning: lt(1)}}, map[time.Duration]time.Duration{10 * time.Minute: 0}},
		"two hours":       {alerting.ThresholdRule{Series: spec, Aggregate: alerting.Last, Window: 2 * time.Hour, Thresholds: alert.Thresholds{Warning: lt(1)}}, map[time.Duration]time.Duration{2 * time.Hour: time.Minute}},
		"prediction":      {alerting.PredictionRule{Series: alerting.SeriesSpec{Metric: "sermon_filesystem_avail_bytes", Dimensions: []string{"mountpoint"}}, Lookback: 24 * time.Hour, Thresholds: alert.Thresholds{Warning: lt(24)}}, map[time.Duration]time.Duration{24 * time.Hour: 5 * time.Minute}},
		"state with seen": {alerting.StateRule{Series: spec, Thresholds: alert.Thresholds{Warning: lt(1)}, Seen: gt(0), SeenWindow: 24 * time.Hour}, map[time.Duration]time.Duration{5 * time.Minute: 0, 24 * time.Hour: 5 * time.Minute}},
	} {
		m := &fakeMetrics{series: map[string][]metrics.Series{}}
		if _, err := tc.rule.Evaluate(context.Background(), env(m, webNode)); err != nil {
			t.Fatal(err)
		}
		got := map[time.Duration]time.Duration{}
		for _, q := range m.queries {
			got[q.To.Sub(q.From)] = q.Step
		}
		if len(got) != len(tc.steps) {
			t.Errorf("%s: queries %v, want %v", name, got, tc.steps)
		}
		for span, step := range tc.steps {
			if s, ok := got[span]; !ok || s != step {
				t.Errorf("%s: %v queried with step %v, want %v", name, span, s, step)
			}
		}
	}
}
