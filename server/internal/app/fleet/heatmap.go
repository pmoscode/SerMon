package fleet

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// The fleet heatmap (KONZEPT §16, 7; M4-27): nodes × time, one ratio per cell.
const (
	// MaxHeatmapColumns bounds the columns; the server sums the points up to them.
	MaxHeatmapColumns = 200
	// MaxHeatmapRange is the longest range, MinHeatmapRange the shortest.
	MaxHeatmapRange = 30 * 24 * time.Hour
	MinHeatmapRange = 10 * time.Minute
	// MaxHeatmapRows bounds the nodes of one heatmap.
	MaxHeatmapRows = metrics.MaxNodes
	// heatmapChunk nodes are read at a time: a node may have several disks, a query returns at
	// most metrics.MaxSeries series.
	heatmapChunk = 20
)

// ErrInvalidHeatmap means an unknown kind or a range outside the limits.
var ErrInvalidHeatmap = errors.New("the heatmap needs a kind and a range of 10 minutes to 30 days")

// HeatmapKind is what colours the cells.
type HeatmapKind int

// The kinds of a heatmap; every value is a ratio 0..1.
const (
	HeatmapCPU     HeatmapKind = iota + 1 // CPU busy (1 − idle)
	HeatmapMemory                         // memory used
	HeatmapIO                             // the busiest disk's utilization
	HeatmapAnomaly                        // the anomaly rate (ADR 0037)
)

// heatmapSteps are the column widths; each divides a day, so columns start at the same instants
// as the store's buckets.
var heatmapSteps = []time.Duration{
	10 * time.Second, time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour,
}

// HeatmapGrid returns the columns of [from, to): the first column's start (from truncated to the
// step), the step and their number, at most MaxHeatmapColumns. ok is false for a range outside the
// limits.
func HeatmapGrid(from, to time.Time) (start time.Time, step time.Duration, columns int, ok bool) {
	span := to.Sub(from)
	if span < MinHeatmapRange || span > MaxHeatmapRange {
		return time.Time{}, 0, 0, false
	}
	for _, step = range heatmapSteps {
		start = from.UTC().Truncate(step)
		columns = int((to.Sub(start) + step - 1) / step)
		if columns <= MaxHeatmapColumns {
			return start, step, columns, true
		}
	}
	return time.Time{}, 0, 0, false
}

// Missing marks a cell without data.
const Missing = -1

// HeatmapCells puts the points of a node's series into columns: per column the highest of the
// series' averages (one series per node but for IO: the busiest disk), Missing without a point.
// Points outside the columns are left out.
func HeatmapCells(series [][]metrics.Point, start time.Time, step time.Duration, columns int, value func(float64) float64) []float64 {
	out := make([]float64, columns)
	for i := range out {
		out[i] = Missing
	}
	for _, points := range series {
		for _, p := range points {
			if p.Time.Before(start) {
				continue
			}
			i := int(p.Time.Sub(start) / step)
			if i >= columns || math.IsNaN(p.Avg) {
				continue
			}
			out[i] = max(out[i], min(max(value(p.Avg), 0), 1))
		}
	}
	return out
}

// HeatmapRow is a node with its cells, oldest first.
type HeatmapRow struct {
	Node     identity.NodeID
	Hostname string
	Group    string
	Cells    []float64
}

// Heatmap is the fleet heatmap.
type Heatmap struct {
	Start   time.Time
	Step    time.Duration
	Columns int
	Rows    []HeatmapRow
	// Truncated: more nodes than MaxHeatmapRows (the first by host name are shown).
	Truncated bool
}

type heatmapSource struct {
	metric string
	labels metrics.Labels
	value  func(float64) float64
}

var heatmapSources = map[HeatmapKind]heatmapSource{
	HeatmapCPU:     {"sermon_cpu_utilization_ratio", metrics.Labels{"mode": "idle"}, func(v float64) float64 { return 1 - v/1e6 }},
	HeatmapMemory:  {"sermon_memory_utilization_ratio", nil, func(v float64) float64 { return v / 1e6 }},
	HeatmapIO:      {"sermon_disk_busy_ratio", nil, func(v float64) float64 { return v / 1e6 }},
	HeatmapAnomaly: {"sermon_anomaly_rate_ratio", nil, func(v float64) float64 { return v / 1e6 }},
}

// Heatmap returns the heatmap of the visible nodes for kind in [from, to), rows by host name.
func (s *Service) Heatmap(ctx context.Context, c access.Caller, kind HeatmapKind, from, to time.Time) (Heatmap, error) {
	source, known := heatmapSources[kind]
	start, step, columns, ok := HeatmapGrid(from, to)
	if !known || !ok {
		return Heatmap{}, ErrInvalidHeatmap
	}
	scope, nodes, err := s.visible(ctx, c)
	if err != nil {
		return Heatmap{}, err
	}
	slices.SortStableFunc(nodes, func(a, b Node) int { return cmp.Compare(a.Hostname, b.Hostname) })
	out := Heatmap{Start: start, Step: step, Columns: columns}
	if len(nodes) > MaxHeatmapRows {
		nodes, out.Truncated = nodes[:MaxHeatmapRows], true
	}
	for chunk := range slices.Chunk(nodes, heatmapChunk) {
		found, err := s.reader.Query(ctx, scope, metrics.Query{
			Metric: source.metric, Nodes: ids(chunk), Labels: source.labels, From: start, To: start.Add(time.Duration(columns) * step), Step: step,
		})
		if err != nil {
			return Heatmap{}, fmt.Errorf("heatmap of %s: %w", source.metric, err)
		}
		byNode := map[identity.NodeID][][]metrics.Point{}
		for _, one := range found {
			byNode[one.Node] = append(byNode[one.Node], one.Points)
		}
		for _, n := range chunk {
			out.Rows = append(out.Rows, HeatmapRow{
				Node: n.ID, Hostname: n.Hostname, Group: n.GroupName,
				Cells: HeatmapCells(byNode[n.ID], start, step, columns, source.value),
			})
		}
	}
	return out, nil
}
