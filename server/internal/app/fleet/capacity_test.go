package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// filesystem puts a file system's current values into the ring and 24 h of its available bytes
// into the store, losing perHour each hour (0: flat).
func (w world) filesystem(t *testing.T, org tenant.OrgID, group tenant.GroupID, node identity.NodeID, mountpoint string, size, avail, perHour float64, readonly bool) {
	t.Helper()
	labels := metrics.Labels{"mountpoint": mountpoint}
	w.live(org, group, node, "sermon_filesystem_size_bytes", labels, size)
	w.live(org, group, node, "sermon_filesystem_avail_bytes", labels, avail)
	w.live(org, group, node, "sermon_filesystem_utilization_ratio", labels, (1-avail/size)*1e6)
	ro := 0.0
	if readonly {
		ro = 1
	}
	w.live(org, group, node, "sermon_filesystem_readonly", labels, ro)
	var history []metrics.Aggregate
	for hour := 24; hour >= 1; hour-- {
		v := avail + float64(hour)*perHour
		history = append(history, metrics.Aggregate{Node: node, Metric: "sermon_filesystem_avail_bytes", Labels: labels, Start: now.Add(-time.Duration(hour) * time.Hour), Avg: v, Min: v, Max: v})
	}
	if err := w.store.Write(context.Background(), org, history, nil); err != nil {
		t.Fatal(err)
	}
}

// daily writes one value per day of the last 30 days, rising from first to last.
func (w world) daily(t *testing.T, org tenant.OrgID, node identity.NodeID, metric string, labels metrics.Labels, first, last float64) {
	t.Helper()
	var points []metrics.Aggregate
	start := now.Add(-fleet.TrendRange).Truncate(fleet.TrendStep)
	for day := range 30 {
		v := first + (last-first)*float64(day)/29
		points = append(points, metrics.Aggregate{Node: node, Metric: metric, Labels: labels, Start: start.Add(time.Duration(day)*fleet.TrendStep + time.Hour), Avg: v, Min: v, Max: v})
	}
	if err := w.store.Write(context.Background(), org, points, nil); err != nil {
		t.Fatal(err)
	}
}

// M4-26: every visible file system with its forecast, the one running full first, those without
// a forecast by usage; read-only ones get none. The daily CPU busy and memory of each node over
// 30 days. Nothing of other groups or organizations.
func TestCapacity_ForecastsAndTrendsOfTheVisibleNodes(t *testing.T) {
	w := newWorld()
	w.filesystem(t, orgA, web, webN, "/", 100e9, 30e9, 1e9, false)       // full in ~30 h
	w.filesystem(t, orgA, web, webN, "/var", 100e9, 50e9, 0.1e9, false)  // full in ~500 h
	w.filesystem(t, orgA, web, webN, "/boot", 1e9, 0.1e9, 0, false)      // flat, 90 % used
	w.filesystem(t, orgA, web, webN, "/srv", 100e9, 80e9, 0, false)      // flat, 20 % used
	w.filesystem(t, orgA, web, webN, "/mnt/iso", 10e9, 0.5e9, 1e9, true) // read-only, 95 % used
	w.filesystem(t, orgA, db, dbN, "/", 100e9, 10e9, 1e9, false)         // another group
	w.filesystem(t, orgB, other, otherN, "/", 100e9, 1e9, 1e9, false)    // another organization
	w.daily(t, orgA, webN, "sermon_cpu_utilization_ratio", metrics.Labels{"mode": "idle"}, 900_000, 600_000)
	w.daily(t, orgA, webN, "sermon_cpu_utilization_ratio", metrics.Labels{"mode": "user"}, 100_000, 400_000)
	w.daily(t, orgA, webN, "sermon_memory_utilization_ratio", nil, 400_000, 700_000)
	w.daily(t, orgA, dbN, "sermon_memory_utilization_ratio", nil, 500_000, 500_000)

	got, err := w.svc.Capacity(context.Background(), viewerAW)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, fs := range got.Filesystems {
		order = append(order, fs.Hostname+":"+fs.Mountpoint)
		if fs.Node != webN || fs.Group != "web" {
			t.Errorf("not of the caller's group: %+v", fs)
		}
	}
	want := []string{"web-01:/", "web-01:/var", "web-01:/mnt/iso", "web-01:/boot", "web-01:/srv"}
	if len(order) != len(want) {
		t.Fatalf("file systems = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("file systems = %v, want %v", order, want)
		}
	}
	root := got.Filesystems[0]
	if root.FullAt == nil || root.FullAt.Sub(now) < 29*time.Hour || root.FullAt.Sub(now) > 31*time.Hour || root.Size != 100e9 || root.Avail != 30e9 || math.Abs(root.UsedRatio-0.7) > 1e-6 {
		t.Errorf("root = %+v", root)
	}
	if iso := got.Filesystems[2]; iso.FullAt != nil || !iso.Readonly {
		t.Errorf("a read-only file system got a forecast: %+v", iso)
	}
	if got.Truncated {
		t.Error("truncated")
	}
	if len(got.Trends) != 1 || got.Trends[0].Node != webN || got.Trends[0].Hostname != "web-01" {
		t.Fatalf("trends = %+v", got.Trends)
	}
	tr := got.Trends[0]
	if len(tr.CPU) < 29 || len(tr.Memory) < 29 {
		t.Fatalf("%d CPU and %d memory days", len(tr.CPU), len(tr.Memory))
	}
	if first, last := tr.CPU[0].Value, tr.CPU[len(tr.CPU)-1].Value; math.Abs(first-0.1) > 0.02 || math.Abs(last-0.4) > 0.02 {
		t.Errorf("CPU busy from %v to %v, want 1 − idle", first, last)
	}
	if first, last := tr.Memory[0].Value, tr.Memory[len(tr.Memory)-1].Value; math.Abs(first-0.4) > 0.02 || math.Abs(last-0.7) > 0.02 {
		t.Errorf("memory from %v to %v", first, last)
	}
	for i := 1; i < len(tr.CPU); i++ {
		if tr.CPU[i].Day.Sub(tr.CPU[i-1].Day) != fleet.TrendStep {
			t.Fatalf("not daily: %v, %v", tr.CPU[i-1].Day, tr.CPU[i].Day)
		}
	}

	// The admin of the organization sees both nodes; db-01 runs full first.
	all, err := w.svc.Capacity(context.Background(), adminA)
	if err != nil || len(all.Filesystems) != 6 || all.Filesystems[0].Node != dbN || len(all.Trends) != 2 || all.Trends[0].Hostname != "db-01" {
		t.Errorf("admin: %d file systems, first %+v, %d trends, %v", len(all.Filesystems), all.Filesystems, len(all.Trends), err)
	}
	// Without a role nothing is visible, as in the overview.
	if none, err := w.svc.Capacity(context.Background(), caller(orgA)); (err != nil && !errors.Is(err, access.ErrForbidden)) || len(none.Filesystems)+len(none.Trends) != 0 {
		t.Errorf("without a role: %+v, %v", none, err)
	}
}

// Above MaxCapacityFilesystems the answer is cut, those running full first; nodes with the most
// file systems an agent reports are read completely.
func TestCapacity_BoundsTheFilesystems(t *testing.T) {
	store := memstore.New()
	ring := metrics.NewRing()
	var dir directory
	for n := range 9 {
		node := identity.NodeID(uuid.New())
		store.AddNode(orgA, web, node)
		dir = append(dir, record{orgA, fleet.Node{ID: node, Group: web, GroupName: "web", Hostname: fmt.Sprintf("node-%d", n)}})
		w := world{store: store, ring: ring}
		for i := range 60 {
			perHour := 0.0
			if n == 8 && i == 59 {
				perHour = 1e9 // the very last one runs full
			}
			w.filesystem(t, orgA, web, node, fmt.Sprintf("/data/%02d", i), 100e9, 50e9, perHour, false)
		}
	}
	clock := func() time.Time { return now }
	svc := fleet.NewService(dir, metrics.NewReader(store, ring, clock), clock)
	got, err := svc.Capacity(context.Background(), viewerAW)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Filesystems) != fleet.MaxCapacityFilesystems || !got.Truncated {
		t.Fatalf("%d file systems, truncated %v", len(got.Filesystems), got.Truncated)
	}
	if first := got.Filesystems[0]; first.FullAt == nil || first.Hostname != "node-8" || first.Mountpoint != "/data/59" {
		t.Errorf("first = %+v", first)
	}
	// Without daily values a node has no trend.
	if len(got.Trends) != 0 {
		t.Errorf("trends without data: %+v", got.Trends)
	}
}

// M4-31: the trends are read of at most MaxCapacityTrends nodes, the first by host name; the
// answer says when there were more.
func TestCapacity_BoundsTheTrends(t *testing.T) {
	store := memstore.New()
	var dir directory
	nodes := map[int]identity.NodeID{}
	for n := range fleet.MaxCapacityTrends + 1 {
		node := identity.NodeID(uuid.New())
		nodes[n] = node
		store.AddNode(orgA, web, node)
		// In reverse: the directory's order is not the host names'.
		dir = append(directory{record{orgA, fleet.Node{ID: node, Group: web, GroupName: "web", Hostname: fmt.Sprintf("node-%04d", n)}}}, dir...)
	}
	w := world{store: store, ring: metrics.NewRing()}
	w.daily(t, orgA, nodes[0], "sermon_memory_utilization_ratio", nil, 500_000, 500_000)
	w.daily(t, orgA, nodes[fleet.MaxCapacityTrends], "sermon_memory_utilization_ratio", nil, 500_000, 500_000)
	clock := func() time.Time { return now }
	svc := fleet.NewService(dir, metrics.NewReader(store, w.ring, clock), clock)
	got, err := svc.Capacity(context.Background(), viewerAW)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TrendsTruncated || len(got.Trends) != 1 || got.Trends[0].Hostname != "node-0000" {
		t.Errorf("trends truncated %v, %d trends: %+v", got.TrendsTruncated, len(got.Trends), got.Trends)
	}
}
