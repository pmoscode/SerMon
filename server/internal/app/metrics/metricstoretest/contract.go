// Package metricstoretest holds the contract tests every metrics.Store implementation must pass
// (ADR 0001: a replacement such as VictoriaMetrics must behave the same, including tenant
// isolation).
package metricstoretest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Fixture prepares the data the store depends on.
type Fixture interface {
	// Node creates a node of org in group (creating org and group if needed).
	Node(t *testing.T, org tenant.OrgID, group tenant.GroupID) identity.NodeID
	// Settle moves written data to its long-term form: compression and refreshed aggregates.
	Settle(t *testing.T)
}

// Factory returns a fresh store and its fixture.
type Factory func(t *testing.T) (metrics.Store, Fixture)

// Run runs all contract tests.
func Run(t *testing.T, factory Factory) {
	tests := map[string]func(*testing.T, Factory){
		"WritesAndReadsBack":                    writesAndReadsBack,
		"BucketsByStep":                         bucketsByStep,
		"RespectsTheTimeRange":                  respectsTheTimeRange,
		"FiltersByLabels":                       filtersByLabels,
		"OtherOrganizationsSeeNothing":          otherOrganizationsSeeNothing,
		"GroupScopeHidesOtherGroups":            groupScopeHidesOtherGroups,
		"RejectsNodesOfOtherOrganizations":      rejectsNodesOfOtherOrganizations,
		"RecordsTheHighestPersistedSeq":         recordsTheHighestPersistedSeq,
		"AggregatesAndCompressedDataStayScoped": aggregatesAndCompressedDataStayScoped,
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) { test(t, factory) })
	}
}

const metric = "sermon_cpu_utilization_ratio"

// base is a recent window start, so that every retention still holds the data.
func base() time.Time { return time.Now().Add(-30 * time.Minute).Truncate(time.Minute) }

func newOrg() tenant.OrgID     { return tenant.OrgID(uuid.New()) }
func newGroup() tenant.GroupID { return tenant.GroupID(uuid.New()) }

func scopeAll(t *testing.T, org tenant.OrgID) tenant.Scope {
	t.Helper()
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

// windows returns n consecutive 10 s aggregates with avg = i, min = i-1, max = i+1.
func windows(node identity.NodeID, labels metrics.Labels, start time.Time, n int) []metrics.Aggregate {
	out := make([]metrics.Aggregate, n)
	for i := range out {
		v := float64(i)
		out[i] = metrics.Aggregate{
			Node: node, Metric: metric, Labels: labels, Start: start.Add(time.Duration(i) * metrics.Window),
			Avg: v, Min: v - 1, Max: v + 1,
		}
	}
	return out
}

func write(t *testing.T, store metrics.Store, org tenant.OrgID, aggregates []metrics.Aggregate) {
	t.Helper()
	if err := store.Write(context.Background(), org, aggregates, nil); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func query(t *testing.T, store metrics.Store, scope tenant.Scope, q metrics.Query) []metrics.Series {
	t.Helper()
	if q.Metric == "" {
		q.Metric = metric
	}
	if q.Source == 0 {
		q.Source = metrics.SourceRaw
	}
	if q.Step == 0 {
		q.Step = metrics.Window
	}
	series, err := store.Query(context.Background(), scope, q)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	return series
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func writesAndReadsBack(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	org := newOrg()
	node := fixture.Node(t, org, newGroup())
	start := base()
	write(t, store, org, windows(node, metrics.Labels{"mode": "user"}, start, 6))

	series := query(t, store, scopeAll(t, org), metrics.Query{Nodes: []identity.NodeID{node}, From: start, To: start.Add(time.Minute)})

	if len(series) != 1 || series[0].Node != node || series[0].Labels["mode"] != "user" {
		t.Fatalf("series = %+v", series)
	}
	points := series[0].Points
	if len(points) != 6 {
		t.Fatalf("points = %d, want 6", len(points))
	}
	for i, p := range points {
		v := float64(i)
		if !p.Time.Equal(start.Add(time.Duration(i)*metrics.Window)) || !near(p.Avg, v) || !near(p.Min, v-1) || !near(p.Max, v+1) {
			t.Errorf("point %d = %+v", i, p)
		}
	}
}

func bucketsByStep(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	org := newOrg()
	node := fixture.Node(t, org, newGroup())
	start := base()
	write(t, store, org, windows(node, nil, start, 6))

	series := query(t, store, scopeAll(t, org), metrics.Query{
		Nodes: []identity.NodeID{node}, From: start, To: start.Add(time.Minute), Step: 30 * time.Second,
	})

	if len(series) != 1 || len(series[0].Points) != 2 {
		t.Fatalf("series = %+v, want 2 buckets", series)
	}
	first, second := series[0].Points[0], series[0].Points[1]
	// Bucket 1: windows 0..2 (avg 1, min -1, max 3); bucket 2: windows 3..5 (avg 4, min 2, max 6).
	if !near(first.Avg, 1) || !near(first.Min, -1) || !near(first.Max, 3) ||
		!near(second.Avg, 4) || !near(second.Min, 2) || !near(second.Max, 6) {
		t.Errorf("buckets = %+v, %+v", first, second)
	}
}

func respectsTheTimeRange(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	org := newOrg()
	node := fixture.Node(t, org, newGroup())
	start := base()
	write(t, store, org, windows(node, nil, start, 6))

	series := query(t, store, scopeAll(t, org), metrics.Query{
		Nodes: []identity.NodeID{node}, From: start.Add(20 * time.Second), To: start.Add(40 * time.Second),
	})

	if len(series) != 1 || len(series[0].Points) != 2 || !series[0].Points[0].Time.Equal(start.Add(20*time.Second)) {
		t.Errorf("series = %+v, want windows at 20 s and 30 s (To is exclusive)", series)
	}
}

func filtersByLabels(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	org := newOrg()
	node := fixture.Node(t, org, newGroup())
	start := base()
	write(t, store, org, append(
		windows(node, metrics.Labels{"mode": "user", "cpu": "all"}, start, 2),
		windows(node, metrics.Labels{"mode": "system", "cpu": "all"}, start, 2)...,
	))

	all := query(t, store, scopeAll(t, org), metrics.Query{Nodes: []identity.NodeID{node}, From: start, To: start.Add(time.Minute)})
	user := query(t, store, scopeAll(t, org), metrics.Query{
		Nodes: []identity.NodeID{node}, Labels: metrics.Labels{"mode": "user"}, From: start, To: start.Add(time.Minute),
	})

	if len(all) != 2 {
		t.Errorf("all series = %d, want 2", len(all))
	}
	if len(user) != 1 || user[0].Labels["mode"] != "user" || user[0].Labels["cpu"] != "all" {
		t.Errorf("filtered = %+v", user)
	}
}

func otherOrganizationsSeeNothing(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	orgA, orgB := newOrg(), newOrg()
	nodeA := fixture.Node(t, orgA, newGroup())
	nodeB := fixture.Node(t, orgB, newGroup())
	start := base()
	write(t, store, orgA, windows(nodeA, nil, start, 3))
	write(t, store, orgB, windows(nodeB, nil, start, 3))

	foreign := query(t, store, scopeAll(t, orgB), metrics.Query{Nodes: []identity.NodeID{nodeA}, From: start, To: start.Add(time.Minute)})
	mixed := query(t, store, scopeAll(t, orgB), metrics.Query{Nodes: []identity.NodeID{nodeA, nodeB}, From: start, To: start.Add(time.Minute)})

	if len(foreign) != 0 {
		t.Errorf("org B sees series of org A: %+v", foreign)
	}
	if len(mixed) != 1 || mixed[0].Node != nodeB {
		t.Errorf("mixed query = %+v, want only node B", mixed)
	}
}

func groupScopeHidesOtherGroups(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	org := newOrg()
	web, db := newGroup(), newGroup()
	webNode := fixture.Node(t, org, web)
	dbNode := fixture.Node(t, org, db)
	start := base()
	write(t, store, org, append(windows(webNode, nil, start, 2), windows(dbNode, nil, start, 2)...))
	scope, err := tenant.ForGroups(org, web)
	if err != nil {
		t.Fatal(err)
	}

	series := query(t, store, scope, metrics.Query{Nodes: []identity.NodeID{webNode, dbNode}, From: start, To: start.Add(time.Minute)})

	if len(series) != 1 || series[0].Node != webNode {
		t.Errorf("series = %+v, want only the node of the allowed group", series)
	}
}

func rejectsNodesOfOtherOrganizations(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	orgA, orgB := newOrg(), newOrg()
	nodeA := fixture.Node(t, orgA, newGroup())
	fixture.Node(t, orgB, newGroup())
	start := base()

	err := store.Write(context.Background(), orgB, windows(nodeA, nil, start, 2), map[identity.NodeID]uint64{nodeA: 7})

	if err == nil {
		t.Fatal("Write() for a node of another organization succeeded")
	}
	if series := query(t, store, scopeAll(t, orgA), metrics.Query{Nodes: []identity.NodeID{nodeA}, From: start, To: start.Add(time.Minute)}); len(series) != 0 {
		t.Errorf("rejected write stored data: %+v", series)
	}
	if seq, err := store.PersistedSeq(context.Background(), orgA, nodeA); err != nil || seq != 0 {
		t.Errorf("PersistedSeq() = %d, %v; want 0", seq, err)
	}
}

func recordsTheHighestPersistedSeq(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	org := newOrg()
	node := fixture.Node(t, org, newGroup())
	ctx := context.Background()

	if err := store.Write(ctx, org, windows(node, nil, base(), 1), map[identity.NodeID]uint64{node: 42}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, org, nil, map[identity.NodeID]uint64{node: 41}); err != nil {
		t.Fatal(err)
	}

	if seq, err := store.PersistedSeq(ctx, org, node); err != nil || seq != 42 {
		t.Errorf("PersistedSeq() = %d, %v; want 42 (never decreases)", seq, err)
	}
	if _, err := store.PersistedSeq(ctx, newOrg(), node); err == nil {
		t.Error("PersistedSeq() for another organization must fail")
	}
}

func aggregatesAndCompressedDataStayScoped(t *testing.T, factory Factory) {
	store, fixture := factory(t)
	orgA, orgB := newOrg(), newOrg()
	nodeA := fixture.Node(t, orgA, newGroup())
	fixture.Node(t, orgB, newGroup())
	start := base()
	write(t, store, orgA, windows(nodeA, nil, start, 6))
	fixture.Settle(t)

	for _, source := range []metrics.Source{metrics.SourceRaw, metrics.Source1m} {
		q := metrics.Query{Nodes: []identity.NodeID{nodeA}, From: start, To: start.Add(time.Minute), Source: source, Step: source.Resolution()}
		own := query(t, store, scopeAll(t, orgA), q)
		if len(own) != 1 || len(own[0].Points) == 0 {
			t.Errorf("source %v: own series = %+v", source, own)
		}
		if source == metrics.Source1m && (len(own) != 1 || len(own[0].Points) != 1 || !near(own[0].Points[0].Avg, 2.5) ||
			!near(own[0].Points[0].Min, -1) || !near(own[0].Points[0].Max, 6)) {
			t.Errorf("1 min aggregate = %+v, want avg 2.5, min -1, max 6", own)
		}
		if foreign := query(t, store, scopeAll(t, orgB), q); len(foreign) != 0 {
			t.Errorf("source %v: org B sees %+v", source, foreign)
		}
	}
}
