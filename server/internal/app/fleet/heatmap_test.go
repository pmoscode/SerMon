package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// M4-27: at most 200 columns, the narrowest step that fits; columns start at multiples of the
// step (as the store's buckets) and cover the whole range.
func TestHeatmapGrid_AtTheLimits(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 7, 30, 0, time.UTC)
	for name, tc := range map[string]struct {
		span    time.Duration
		step    time.Duration
		columns int
	}{
		"shortest":                    {10 * time.Minute, 10 * time.Second, 60},
		"200 columns of 10 s":         {33*time.Minute + 20*time.Second, 10 * time.Second, 200},
		"one more":                    {33*time.Minute + 30*time.Second, time.Minute, 34},
		"an hour":                     {time.Hour, time.Minute, 61},
		"a day":                       {24 * time.Hour, 10 * time.Minute, 145},
		"a week":                      {7 * 24 * time.Hour, time.Hour, 169},
		"30 days, 180 columns of 4 h": {30 * 24 * time.Hour, 4 * time.Hour, 181},
	} {
		start, step, columns, ok := fleet.HeatmapGrid(at, at.Add(tc.span))
		if !ok || step != tc.step || columns != tc.columns {
			t.Errorf("%s: step %v, %d columns, %v; want %v, %d", name, step, columns, ok, tc.step, tc.columns)
			continue
		}
		if !start.Equal(at.Truncate(step)) || start.After(at) || start.Add(time.Duration(columns)*step).Before(at.Add(tc.span)) || columns > fleet.MaxHeatmapColumns {
			t.Errorf("%s: columns from %v by %v do not cover the range", name, start, step)
		}
	}
	// Aligned at the start: 200 columns exactly, one minute more needs the next step.
	aligned := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	if _, step, columns, _ := fleet.HeatmapGrid(aligned, aligned.Add(200*time.Minute)); step != time.Minute || columns != 200 {
		t.Errorf("aligned: %v, %d", step, columns)
	}
	if _, step, columns, _ := fleet.HeatmapGrid(aligned, aligned.Add(201*time.Minute)); step != 5*time.Minute || columns != 41 {
		t.Errorf("aligned and a minute more: %v, %d", step, columns)
	}
	for _, span := range []time.Duration{10*time.Minute - time.Second, 30*24*time.Hour + time.Second, 0, -time.Hour} {
		if _, _, _, ok := fleet.HeatmapGrid(at, at.Add(span)); ok {
			t.Errorf("span %v accepted", span)
		}
	}
}

// A point counts in the column its bucket starts in; at the end and before the start it is
// left out. Several series of a node: the highest; values clamped to 0..1, NaN is none.
func TestHeatmapCells_AtTheBoundaries(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	step := 10 * time.Minute
	p := func(minutes int, v float64) metrics.Point {
		return metrics.Point{Time: start.Add(time.Duration(minutes) * time.Minute), Avg: v}
	}
	got := fleet.HeatmapCells([][]metrics.Point{
		{p(-10, 0.9), p(0, 0.2), p(9, 0.3), p(10, 0.4), p(29, math.NaN()), p(30, 0.9)},
		{p(0, 0.1), p(11, 0.6), p(20, 1.5), p(21, -2)},
	}, start, step, 3, func(v float64) float64 { return v })
	if want := []float64{0.3, 0.6, 1}; !slices.Equal(got, want) {
		t.Errorf("cells = %v, want %v", got, want)
	}
	if got := fleet.HeatmapCells(nil, start, step, 2, func(v float64) float64 { return v }); !slices.Equal(got, []float64{fleet.Missing, fleet.Missing}) {
		t.Errorf("without points = %v", got)
	}
}

// The heatmap of the visible nodes by host name: CPU as 1 − idle, IO the busiest disk, the
// anomaly rate from ppm; nodes of other groups and organizations are missing.
func TestHeatmap_KindsAndVisibility(t *testing.T) {
	w := newWorld()
	from := now.Add(-time.Hour)
	var points []metrics.Aggregate
	add := func(node identity.NodeID, metric string, labels metrics.Labels, minute int, v float64) {
		points = append(points, metrics.Aggregate{Node: node, Metric: metric, Labels: labels, Start: from.Add(time.Duration(minute) * time.Minute), Avg: v, Min: v, Max: v})
	}
	add(webN, "sermon_cpu_utilization_ratio", metrics.Labels{"mode": "idle"}, 5, 700_000)
	add(webN, "sermon_cpu_utilization_ratio", metrics.Labels{"mode": "user"}, 5, 300_000)
	add(webN, "sermon_disk_busy_ratio", metrics.Labels{"device": "vda"}, 5, 200_000)
	add(webN, "sermon_disk_busy_ratio", metrics.Labels{"device": "vdb"}, 5, 800_000)
	add(webN, "sermon_anomaly_rate_ratio", metrics.Labels{}, 30, 250_000)
	add(dbN, "sermon_memory_utilization_ratio", metrics.Labels{}, 5, 500_000)
	if err := w.store.Write(context.Background(), orgA, points, nil); err != nil {
		t.Fatal(err)
	}
	cell := func(h fleet.Heatmap, row, minute int) float64 {
		return h.Rows[row].Cells[int(from.Add(time.Duration(minute)*time.Minute).Sub(h.Start)/h.Step)]
	}
	cpu, err := w.svc.Heatmap(context.Background(), viewerAW, fleet.HeatmapCPU, from, now)
	if err != nil || len(cpu.Rows) != 1 || cpu.Rows[0].Node != webN || cpu.Rows[0].Hostname != "web-01" || cpu.Columns != 60 || cpu.Step != time.Minute {
		t.Fatalf("cpu = %+v, %v", cpu, err)
	}
	if v := cell(cpu, 0, 5); math.Abs(v-0.3) > 1e-9 {
		t.Errorf("cpu busy = %v, want 1 − idle", v)
	}
	if v := cell(cpu, 0, 6); v != fleet.Missing {
		t.Errorf("an empty minute = %v", v)
	}
	if io, _ := w.svc.Heatmap(context.Background(), viewerAW, fleet.HeatmapIO, from, now); cell(io, 0, 5) != 0.8 {
		t.Errorf("io = %v, want the busiest disk", cell(io, 0, 5))
	}
	if an, _ := w.svc.Heatmap(context.Background(), viewerAW, fleet.HeatmapAnomaly, from, now); cell(an, 0, 30) != 0.25 {
		t.Errorf("anomaly rate = %v", cell(an, 0, 30))
	}
	all, err := w.svc.Heatmap(context.Background(), adminA, fleet.HeatmapMemory, from, now)
	if err != nil || len(all.Rows) != 2 || all.Rows[0].Hostname != "db-01" || cell(all, 0, 5) != 0.5 || all.Truncated {
		t.Errorf("admin = %+v, %v", all.Rows, err)
	}
	for _, bad := range []struct {
		kind     fleet.HeatmapKind
		from, to time.Time
	}{{0, from, now}, {9, from, now}, {fleet.HeatmapCPU, now, from}, {fleet.HeatmapCPU, now.Add(-31 * 24 * time.Hour), now}} {
		if _, err := w.svc.Heatmap(context.Background(), viewerAW, bad.kind, bad.from, bad.to); !errors.Is(err, fleet.ErrInvalidHeatmap) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

// Nodes with many disks: read in chunks small enough for the series limit of a query.
func TestHeatmap_ReadsNodesWithManyDisks(t *testing.T) {
	store := memstore.New()
	var dir directory
	var points []metrics.Aggregate
	for i := range 30 {
		node := identity.NodeID(uuid.New())
		store.AddNode(orgA, web, node)
		dir = append(dir, record{orgA, fleet.Node{ID: node, Group: web, Hostname: fmt.Sprintf("node-%02d", i)}})
		for d := range 20 {
			points = append(points, metrics.Aggregate{Node: node, Metric: "sermon_disk_busy_ratio", Labels: metrics.Labels{"device": fmt.Sprintf("sd%c", 'a'+d)}, Start: now.Add(-30 * time.Minute), Avg: float64(d) * 10_000, Min: 0, Max: 0})
		}
	}
	if err := store.Write(context.Background(), orgA, points, nil); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	svc := fleet.NewService(dir, metrics.NewReader(store, metrics.NewRing(), clock), clock)
	h, err := svc.Heatmap(context.Background(), viewerAW, fleet.HeatmapIO, now.Add(-time.Hour), now)
	if err != nil || len(h.Rows) != 30 {
		t.Fatalf("%d rows, %v", len(h.Rows), err)
	}
	for _, r := range h.Rows {
		if !slices.Contains(r.Cells, 0.19) {
			t.Errorf("%s: the busiest of its 20 disks is missing", r.Hostname)
		}
	}
}

// At most MaxHeatmapRows nodes, the first by host name; all of them read (in chunks).
func TestHeatmap_BoundsTheRows(t *testing.T) {
	store := memstore.New()
	var dir directory
	var points []metrics.Aggregate
	for i := range fleet.MaxHeatmapRows + 2 {
		node := identity.NodeID(uuid.New())
		store.AddNode(orgA, web, node)
		dir = append(dir, record{orgA, fleet.Node{ID: node, Group: web, Hostname: fmt.Sprintf("node-%03d", fleet.MaxHeatmapRows+1-i)}})
		points = append(points, metrics.Aggregate{Node: node, Metric: "sermon_memory_utilization_ratio", Labels: metrics.Labels{}, Start: now.Add(-30 * time.Minute), Avg: 400_000, Min: 400_000, Max: 400_000})
	}
	if err := store.Write(context.Background(), orgA, points, nil); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	svc := fleet.NewService(dir, metrics.NewReader(store, metrics.NewRing(), clock), clock)
	h, err := svc.Heatmap(context.Background(), viewerAW, fleet.HeatmapMemory, now.Add(-time.Hour), now)
	if err != nil || len(h.Rows) != fleet.MaxHeatmapRows || !h.Truncated || h.Rows[0].Hostname != "node-000" {
		t.Fatalf("%d rows, truncated %v, first %q, %v", len(h.Rows), h.Truncated, h.Rows[0].Hostname, err)
	}
	for _, r := range h.Rows {
		if !slices.Contains(r.Cells, 0.4) {
			t.Fatalf("%s has no value: a chunk was not read", r.Hostname)
		}
	}
}
