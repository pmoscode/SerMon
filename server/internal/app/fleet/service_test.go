package fleet_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	web    = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	db     = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
	other  = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000cc"))
	webN   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	dbN    = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	otherN = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000003"))
)

type record struct {
	org  tenant.OrgID
	node fleet.Node
}

type directory []record

func (d directory) List(_ context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	var out []fleet.Node
	for _, r := range d {
		if r.org == scope.OrgID() && scope.AllowsGroup(r.node.Group) {
			out = append(out, r.node)
		}
	}
	return out, nil
}

func (d directory) HasGroup(_ context.Context, scope tenant.Scope, group tenant.GroupID) (bool, error) {
	for _, r := range d {
		if r.org == scope.OrgID() && r.node.Group == group && scope.AllowsGroup(group) {
			return true, nil
		}
	}
	return false, nil
}

type world struct {
	store *memstore.Store
	ring  *metrics.Ring
	svc   *fleet.Service
}

func newWorld() world {
	recent := now.Add(-10 * time.Second)
	old := now.Add(-time.Hour)
	dir := directory{
		{orgA, fleet.Node{ID: webN, Group: web, GroupName: "web", Hostname: "web-01", LastSeenAt: &recent}},
		{orgA, fleet.Node{ID: dbN, Group: db, GroupName: "db", Hostname: "db-01", LastSeenAt: &old}},
		{orgB, fleet.Node{ID: otherN, Group: other, GroupName: "other", Hostname: "other-01", LastSeenAt: &recent}},
	}
	store := memstore.New()
	store.AddNode(orgA, web, webN)
	store.AddNode(orgA, db, dbN)
	store.AddNode(orgB, other, otherN)
	ring := metrics.NewRing()
	clock := func() time.Time { return now }
	return world{store: store, ring: ring, svc: fleet.NewService(dir, metrics.NewReader(store, ring, clock), clock)}
}

func (w world) live(org tenant.OrgID, group tenant.GroupID, node identity.NodeID, metric string, labels metrics.Labels, value float64) {
	w.ring.Add(org, group, node, metric, labels, now.Add(-5*time.Second), value)
}

func caller(org tenant.OrgID, bindings ...authz.Binding) access.Caller {
	return access.Caller{Principal: authz.Principal{User: identity.UserID(uuid.New()), Org: org, Bindings: bindings}}
}

var (
	adminA   = caller(orgA, authz.Binding{AllGroups: true, Role: role.Admin})
	viewerAW = caller(orgA, authz.Binding{Group: web, Role: role.Viewer})
	adminB   = caller(orgB, authz.Binding{AllGroups: true, Role: role.Admin})
)

func hostnames(s fleet.Summary) []string {
	var names []string
	for _, n := range s.Nodes {
		names = append(names, n.Hostname)
	}
	return names
}

func TestSummary_ShowsOnlyVisibleNodesWithStatusAndFigures(t *testing.T) {
	w := newWorld()
	w.live(orgA, web, webN, "sermon_cpu_utilization_ratio", metrics.Labels{"mode": "idle"}, 750_000)
	w.live(orgA, web, webN, "sermon_filesystem_utilization_ratio", metrics.Labels{"mountpoint": "/"}, 400_000)
	w.live(orgA, web, webN, "sermon_filesystem_utilization_ratio", metrics.Labels{"mountpoint": "/var"}, 900_000)
	w.live(orgA, web, webN, "sermon_network_receive_bytes_per_second", metrics.Labels{"interface": "eth0"}, 100)
	w.live(orgA, web, webN, "sermon_network_receive_bytes_per_second", metrics.Labels{"interface": "eth1"}, 50)

	all, err := w.svc.Summary(context.Background(), adminA, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hostnames(all), []string{"web-01", "db-01"}) || all.Online != 1 || all.Offline != 1 {
		t.Errorf("admin summary = %v, online %d, offline %d", hostnames(all), all.Online, all.Offline)
	}
	f := all.Nodes[0].Figures
	if f.CPUBusy == nil || *f.CPUBusy != 0.25 || *f.FilesystemMaxUsed != 0.9 || *f.ReceiveBytes != 150 || f.MemoryUsed != nil {
		t.Errorf("figures = %+v", f)
	}
	viewer, err := w.svc.Summary(context.Background(), viewerAW, nil)
	if err != nil || !slices.Equal(hostnames(viewer), []string{"web-01"}) {
		t.Errorf("viewer of web sees %v, %v", hostnames(viewer), err)
	}
	foreign, err := w.svc.Summary(context.Background(), adminB, nil)
	if err != nil || !slices.Equal(hostnames(foreign), []string{"other-01"}) || foreign.Nodes[0].Figures.CPUBusy != nil {
		t.Errorf("org B sees %v (%+v), %v", hostnames(foreign), foreign.Nodes, err)
	}
}

func TestSummary_GroupFilterRejectsForeignGroups(t *testing.T) {
	w := newWorld()
	own, err := w.svc.Summary(context.Background(), adminA, &db)
	if err != nil || !slices.Equal(hostnames(own), []string{"db-01"}) {
		t.Errorf("own group = %v, %v", hostnames(own), err)
	}
	for name, c := range map[string]access.Caller{"other org": adminB, "other group": viewerAW} {
		if _, err := w.svc.Summary(context.Background(), c, &db); !errors.Is(err, access.ErrNotFound) {
			t.Errorf("%s: error = %v, want ErrNotFound", name, err)
		}
	}
}

func TestNode_IsNotFoundOutsideTheScope(t *testing.T) {
	w := newWorld()
	w.live(orgA, web, webN, "sermon_system_uptime_seconds", nil, 3600)

	node, err := w.svc.Node(context.Background(), viewerAW, webN)
	if err != nil || !node.Online || node.Figures.Uptime == nil || *node.Figures.Uptime != 3600 {
		t.Fatalf("Node() = %+v, %v", node, err)
	}
	for name, c := range map[string]access.Caller{"other org": adminB, "other group": viewerAW} {
		if _, err := w.svc.Node(context.Background(), c, dbN); !errors.Is(err, access.ErrNotFound) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if _, err := w.svc.Node(context.Background(), adminB, webN); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B reads a node of org A: %v", err)
	}
}

func TestQuery_ReadsVisibleNodesAndRejectsAnyOther(t *testing.T) {
	w := newWorld()
	start := now.Add(-2 * time.Hour).Truncate(metrics.Window)
	if err := w.store.Write(context.Background(), orgA, []metrics.Aggregate{{Node: webN, Metric: "sermon_load_average", Labels: metrics.Labels{"period": "1m"}, Start: start, Avg: 2, Min: 1, Max: 3}}, nil); err != nil {
		t.Fatal(err)
	}
	q := metrics.Query{Metric: "sermon_load_average", Nodes: []identity.NodeID{webN}, From: now.Add(-3 * time.Hour), To: now}

	series, step, err := w.svc.Query(context.Background(), viewerAW, q)
	if err != nil || len(series) != 1 || step != 10*time.Second || series[0].Points[0].Avg != 2 {
		t.Fatalf("Query() = %+v, %v, %v", series, step, err)
	}
	q.Nodes = []identity.NodeID{webN, dbN}
	if _, _, err := w.svc.Query(context.Background(), viewerAW, q); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("mixed visible and invisible nodes: %v, want ErrNotFound", err)
	}
	q.Nodes = []identity.NodeID{webN}
	if _, _, err := w.svc.Query(context.Background(), adminB, q); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B queries org A: %v", err)
	}
}

func TestFilesystems_CurrentValuesAndForecast(t *testing.T) {
	w := newWorld()
	root := metrics.Labels{"mountpoint": "/"}
	w.live(orgA, web, webN, "sermon_filesystem_size_bytes", root, 100e9)
	w.live(orgA, web, webN, "sermon_filesystem_used_bytes", root, 60e9)
	w.live(orgA, web, webN, "sermon_filesystem_avail_bytes", root, 30e9)
	w.live(orgA, web, webN, "sermon_filesystem_utilization_ratio", root, 666_667)
	w.live(orgA, web, webN, "sermon_filesystem_readonly", root, 0)
	// 24 h history: 1 GB less every hour, 30 GB left now → full in about 30 h.
	var history []metrics.Aggregate
	for hour := 24; hour >= 1; hour-- {
		at := now.Add(-time.Duration(hour) * time.Hour)
		avail := 30e9 + float64(hour)*1e9
		history = append(history, metrics.Aggregate{Node: webN, Metric: "sermon_filesystem_avail_bytes", Labels: root, Start: at, Avg: avail, Min: avail, Max: avail})
	}
	if err := w.store.Write(context.Background(), orgA, history, nil); err != nil {
		t.Fatal(err)
	}

	filesystems, err := w.svc.Filesystems(context.Background(), viewerAW, webN)
	if err != nil || len(filesystems) != 1 {
		t.Fatalf("Filesystems() = %+v, %v", filesystems, err)
	}
	fs := filesystems[0]
	if fs.Mountpoint != "/" || fs.Size != 100e9 || fs.Used != 60e9 || fs.Avail != 30e9 || fs.UsedRatio < 0.66 || fs.Readonly {
		t.Errorf("filesystem = %+v", fs)
	}
	if fs.FullAt == nil || fs.FullAt.Sub(now) < 29*time.Hour || fs.FullAt.Sub(now) > 31*time.Hour {
		t.Errorf("full at %v, want in about 30 h", fs.FullAt)
	}
	if _, err := w.svc.Filesystems(context.Background(), adminB, webN); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B: %v", err)
	}
}

func TestWatch_SendsNewPointsAndEndsWithTheClient(t *testing.T) {
	w := newWorld()
	w.live(orgA, web, webN, "sermon_load_average", metrics.Labels{"period": "1m"}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var sent [][]metrics.Series
	q := metrics.Query{Metric: "sermon_load_average", Nodes: []identity.NodeID{webN}}

	err := w.svc.Watch(ctx, viewerAW, q, func(series []metrics.Series) error {
		sent = append(sent, series)
		cancel()
		return nil
	})

	if !errors.Is(err, context.Canceled) || len(sent) != 1 || len(sent[0]) != 1 || sent[0][0].Points[0].Avg != 1 {
		t.Errorf("Watch() = %v after %+v", err, sent)
	}
	if err := w.svc.Watch(context.Background(), adminB, q, func([]metrics.Series) error { return nil }); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B watches org A: %v", err)
	}
}

func TestFullAt(t *testing.T) {
	line := func(start, perHour float64, hours int) []metrics.Point {
		var points []metrics.Point
		for h := range hours {
			at := now.Add(-time.Duration(hours-1-h) * time.Hour)
			points = append(points, metrics.Point{Time: at, Avg: start + perHour*float64(h)})
		}
		return points
	}
	if full, ok := fleet.FullAt(line(100, -10, 6), now, 365*24*time.Hour); !ok || full.Sub(now) != 5*time.Hour {
		t.Errorf("decreasing: %v, %v; want in 5 h", full, ok)
	}
	for name, points := range map[string][]metrics.Point{
		"growing":    line(100, 10, 6),
		"flat":       line(100, 0, 6),
		"few points": line(100, -10, 3),
		"too slow":   line(1e12, -1, 24),
	} {
		if full, ok := fleet.FullAt(points, now, 365*24*time.Hour); ok {
			t.Errorf("%s: forecast %v", name, full)
		}
	}
	if full, ok := fleet.FullAt(line(50, -10, 6), now, 365*24*time.Hour); !ok || !full.Equal(now) {
		t.Errorf("exhausted: %v, %v; want now", full, ok)
	}
}

func TestWatch_SendsEveryPointOnceAlsoWhenItArrivesLate(t *testing.T) {
	w := newWorld()
	labels := metrics.Labels{"period": "1m"}
	w.ring.Add(orgA, web, webN, "sermon_load_average", labels, now.Add(-3*time.Second), 1)
	q := metrics.Query{Metric: "sermon_load_average", Nodes: []identity.NodeID{webN}}
	ctx, cancel := context.WithCancel(context.Background())
	var points []float64
	rounds := 0

	err := w.svc.Watch(ctx, viewerAW, q, func(series []metrics.Series) error {
		for _, one := range series {
			for _, p := range one.Points {
				points = append(points, p.Avg)
			}
		}
		rounds++
		switch rounds {
		case 1:
			// A value for an older second arrives after the first round (agent lag).
			w.ring.Add(orgA, web, webN, "sermon_load_average", labels, now.Add(-2*time.Second), 2)
		case 3:
			cancel()
		}
		return nil
	})

	if !errors.Is(err, context.Canceled) || !slices.Equal(points, []float64{1, 2}) {
		t.Errorf("Watch() = %v with points %v, want 1 and 2 exactly once", err, points)
	}
}
