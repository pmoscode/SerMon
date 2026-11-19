package metrics_test

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	groupW = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	groupD = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
	nodeW  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	nodeD  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
)

func all(t *testing.T, org tenant.OrgID) tenant.Scope {
	t.Helper()
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func ringQuery(from, to time.Time, step time.Duration, nodes ...identity.NodeID) metrics.Query {
	return metrics.Query{Metric: "sermon_load_average", Nodes: nodes, From: from, To: to, Step: step}
}

func TestRing_ReturnsOneValuePerSecondAndBucketsBySteps(t *testing.T) {
	ring := metrics.NewRing()
	start := now.Add(-time.Minute)
	for i := range 10 {
		ring.Add(orgA, groupW, nodeW, "sermon_load_average", metrics.Labels{"period": "1m"}, start.Add(time.Duration(i)*time.Second), float64(i))
	}

	perSecond := ring.Query(all(t, orgA), ringQuery(start, start.Add(10*time.Second), time.Second, nodeW))
	buckets := ring.Query(all(t, orgA), ringQuery(start, start.Add(10*time.Second), 5*time.Second, nodeW))

	if len(perSecond) != 1 || len(perSecond[0].Points) != 10 || perSecond[0].Labels["period"] != "1m" {
		t.Fatalf("per second = %+v", perSecond)
	}
	if len(buckets) != 1 || len(buckets[0].Points) != 2 {
		t.Fatalf("buckets = %+v", buckets)
	}
	first := buckets[0].Points[0]
	if first.Avg != 2 || first.Min != 0 || first.Max != 4 {
		t.Errorf("first bucket = %+v, want avg 2, min 0, max 4", first)
	}
}

func TestRing_OverwritesValuesOlderThanItsSpan(t *testing.T) {
	ring := metrics.NewRing()
	start := now.Add(-20 * time.Minute)
	total := int((20 * time.Minute) / time.Second)
	for i := range total {
		ring.Add(orgA, groupW, nodeW, "sermon_load_average", nil, start.Add(time.Duration(i)*time.Second), float64(i))
	}

	series := ring.Query(all(t, orgA), ringQuery(start, now, time.Second, nodeW))

	if len(series) != 1 || len(series[0].Points) != int(metrics.RingSpan/time.Second) {
		t.Fatalf("points = %d, want %d", len(series[0].Points), int(metrics.RingSpan/time.Second))
	}
	if oldest := series[0].Points[0]; !oldest.Time.Equal(now.Add(-metrics.RingSpan)) {
		t.Errorf("oldest point at %v, want %v", oldest.Time, now.Add(-metrics.RingSpan))
	}
}

func TestRing_RespectsOrganizationGroupAndNodes(t *testing.T) {
	ring := metrics.NewRing()
	at := now.Add(-time.Minute)
	ring.Add(orgA, groupW, nodeW, "sermon_load_average", nil, at, 1)
	ring.Add(orgA, groupD, nodeD, "sermon_load_average", nil, at, 2)
	webOnly, err := tenant.ForGroups(orgA, groupW)
	if err != nil {
		t.Fatal(err)
	}
	q := ringQuery(at, now, time.Second, nodeW, nodeD)

	if got := ring.Query(all(t, orgA), q); len(got) != 2 {
		t.Errorf("own org sees %d series, want 2", len(got))
	}
	if got := ring.Query(all(t, orgB), q); len(got) != 0 {
		t.Errorf("other org sees %+v", got)
	}
	if got := ring.Query(webOnly, q); len(got) != 1 || got[0].Node != nodeW {
		t.Errorf("group scope sees %+v, want only the web node", got)
	}
	if got := ring.Query(tenant.Scope{}, q); len(got) != 0 {
		t.Errorf("invalid scope sees %+v", got)
	}
	if got := ring.Query(all(t, orgA), ringQuery(at, now, time.Second, nodeD)); len(got) != 1 {
		t.Errorf("node filter: %+v", got)
	}
}

func TestRing_PruneForgetsSilentSeries(t *testing.T) {
	ring := metrics.NewRing()
	ring.Add(orgA, groupW, nodeW, "sermon_a", nil, now.Add(-20*time.Minute), 1)
	ring.Add(orgA, groupW, nodeW, "sermon_b", nil, now.Add(-time.Minute), 1)

	ring.Prune(now)

	if ring.Len() != 1 {
		t.Errorf("Len() = %d, want 1", ring.Len())
	}
}

func TestRing_ConcurrentWritersAndReaders(t *testing.T) {
	ring := metrics.NewRing()
	var wg sync.WaitGroup
	for writer := range 4 {
		wg.Go(func() {
			for i := range 500 {
				ring.Add(orgA, groupW, nodeW, "sermon_load_average", metrics.Labels{"w": string(rune('a' + writer))}, now.Add(-time.Duration(i)*time.Second), float64(i))
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			ring.Query(all(t, orgA), ringQuery(now.Add(-10*time.Minute), now, 10*time.Second, nodeW))
			ring.Prune(now)
		}
	})
	wg.Wait()

	if series := ring.Query(all(t, orgA), ringQuery(now.Add(-10*time.Minute), now, time.Second, nodeW)); len(series) != 4 {
		t.Errorf("series = %d, want 4", len(series))
	}
}

func TestReader_RoutesByRangeAndStep(t *testing.T) {
	store := memstore.New()
	store.AddNode(orgA, groupW, nodeW)
	ring := metrics.NewRing()
	ring.Add(orgA, groupW, nodeW, "sermon_load_average", nil, now.Add(-30*time.Second), 7)
	if err := store.Write(context.Background(), orgA, []metrics.Aggregate{{
		Node: nodeW, Metric: "sermon_load_average", Start: now.Add(-40 * time.Minute).Truncate(metrics.Window), Avg: 3, Min: 3, Max: 3,
	}}, nil); err != nil {
		t.Fatal(err)
	}
	reader := metrics.NewReader(store, ring, func() time.Time { return now })

	live, err := reader.Query(context.Background(), all(t, orgA), ringQuery(now.Add(-5*time.Minute), now, time.Second, nodeW))
	if err != nil || len(live) != 1 || live[0].Points[0].Avg != 7 {
		t.Fatalf("live query = %+v, %v; want the ring value", live, err)
	}
	history, err := reader.Query(context.Background(), all(t, orgA), ringQuery(now.Add(-time.Hour), now, time.Minute, nodeW))
	if err != nil || len(history) != 1 || math.Abs(history[0].Points[0].Avg-3) > 1e-9 {
		t.Fatalf("history query = %+v, %v; want the stored window", history, err)
	}
	if _, err := reader.Query(context.Background(), all(t, orgA), ringQuery(now, now, 0, nodeW)); err == nil {
		t.Error("an empty range must be rejected")
	}
}
