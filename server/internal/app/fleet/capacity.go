package fleet

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Capacity planning (KONZEPT §16, 6; M4-26): the fill forecast of every file system and the
// daily CPU and memory of every node over 30 days.
const (
	// TrendRange and TrendStep are the history of the trends.
	TrendRange = 30 * 24 * time.Hour
	TrendStep  = 24 * time.Hour
	// MaxCapacityFilesystems bounds the file systems of one answer (those running full first).
	MaxCapacityFilesystems = 500
	// maxMounts is the most file systems an agent reports (sermon-collectors, MAX_MOUNTS);
	// filesystemChunk nodes have at most metrics.MaxSeries of them, the limit of one query.
	maxMounts       = 64
	filesystemChunk = metrics.MaxSeries / maxMounts
)

// NodeFilesystem is a file system of the fleet: its node and its values with the forecast.
type NodeFilesystem struct {
	Node     identity.NodeID
	Hostname string
	Group    string
	Filesystem
}

// DailyPoint is the average of a day.
type DailyPoint struct {
	Day   time.Time
	Value float64
}

// NodeTrend is the daily CPU busy and memory used (0..1) of a node, oldest first.
type NodeTrend struct {
	Node     identity.NodeID
	Hostname string
	Group    string
	CPU      []DailyPoint
	Memory   []DailyPoint
}

// Capacity is the capacity dashboard.
type Capacity struct {
	// Filesystems sorted by when they run full (no forecast last, then by usage).
	Filesystems []NodeFilesystem
	// Truncated: more file systems than MaxCapacityFilesystems.
	Truncated bool
	Trends    []NodeTrend
}

// Capacity returns the capacity dashboard of the nodes the caller may view.
func (s *Service) Capacity(ctx context.Context, c access.Caller) (Capacity, error) {
	scope, nodes, err := s.visible(ctx, c)
	if err != nil {
		return Capacity{}, err
	}
	var out Capacity
	for chunk := range slices.Chunk(nodes, filesystemChunk) {
		fs, err := s.fleetFilesystems(ctx, scope, chunk)
		if err != nil {
			return Capacity{}, err
		}
		out.Filesystems = append(out.Filesystems, fs...)
	}
	// One series per node and metric.
	for chunk := range slices.Chunk(nodes, metrics.MaxSeries) {
		trends, err := s.trends(ctx, scope, chunk)
		if err != nil {
			return Capacity{}, err
		}
		out.Trends = append(out.Trends, trends...)
	}
	slices.SortStableFunc(out.Filesystems, func(a, b NodeFilesystem) int {
		switch {
		case a.FullAt != nil && b.FullAt != nil:
			if c := a.FullAt.Compare(*b.FullAt); c != 0 {
				return c
			}
		case a.FullAt != nil:
			return -1
		case b.FullAt != nil:
			return 1
		}
		if c := cmp.Compare(b.UsedRatio, a.UsedRatio); c != 0 {
			return c
		}
		return cmp.Or(cmp.Compare(a.Hostname, b.Hostname), cmp.Compare(a.Mountpoint, b.Mountpoint))
	})
	if len(out.Filesystems) > MaxCapacityFilesystems {
		out.Filesystems, out.Truncated = out.Filesystems[:MaxCapacityFilesystems], true
	}
	slices.SortStableFunc(out.Trends, func(a, b NodeTrend) int { return cmp.Compare(a.Hostname, b.Hostname) })
	return out, nil
}

func ids(nodes []Node) []identity.NodeID {
	out := make([]identity.NodeID, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

// fleetFilesystems reads the current values and the forecast of the nodes' file systems.
func (s *Service) fleetFilesystems(ctx context.Context, scope tenant.Scope, nodes []Node) ([]NodeFilesystem, error) {
	type key struct {
		node       identity.NodeID
		mountpoint string
	}
	byNode := map[identity.NodeID]Node{}
	for _, n := range nodes {
		byNode[n.ID] = n
	}
	current := map[key]*NodeFilesystem{}
	get := func(node identity.NodeID, mountpoint string) *NodeFilesystem {
		k := key{node, mountpoint}
		fs, ok := current[k]
		if !ok {
			n := byNode[node]
			fs = &NodeFilesystem{Node: node, Hostname: n.Hostname, Group: n.GroupName, Filesystem: Filesystem{Mountpoint: mountpoint}}
			current[k] = fs
		}
		return fs
	}
	values := map[string]func(*NodeFilesystem, float64){
		"sermon_filesystem_size_bytes":        func(fs *NodeFilesystem, v float64) { fs.Size = toUint(v) },
		"sermon_filesystem_avail_bytes":       func(fs *NodeFilesystem, v float64) { fs.Avail = toUint(v) },
		"sermon_filesystem_utilization_ratio": func(fs *NodeFilesystem, v float64) { fs.UsedRatio = v / 1e6 },
		"sermon_filesystem_readonly":          func(fs *NodeFilesystem, v float64) { fs.Readonly = v > 0 },
	}
	list := ids(nodes)
	// The size first: a file system without one is not current.
	for _, metric := range []string{"sermon_filesystem_size_bytes", "sermon_filesystem_avail_bytes", "sermon_filesystem_utilization_ratio", "sermon_filesystem_readonly"} {
		series, err := s.latest(ctx, scope, metric, list, nil)
		if err != nil {
			return nil, err
		}
		for _, one := range series {
			_, known := current[key{one.Node, one.Labels["mountpoint"]}]
			if value, ok := last(one); ok && (known || metric == "sermon_filesystem_size_bytes") {
				values[metric](get(one.Node, one.Labels["mountpoint"]), value)
			}
		}
	}
	if len(current) == 0 {
		return nil, nil
	}
	now := s.now()
	history, err := s.reader.Query(ctx, scope, metrics.Query{
		Metric: "sermon_filesystem_avail_bytes", Nodes: list,
		From: now.Add(-forecastRange), To: now, Step: forecastStep,
	})
	if err != nil {
		return nil, fmt.Errorf("filesystem history: %w", err)
	}
	for _, one := range history {
		if fs, ok := current[key{one.Node, one.Labels["mountpoint"]}]; ok && !fs.Readonly {
			if full, ok := FullAt(one.Points, now, forecastHorizon); ok {
				fs.FullAt = &full
			}
		}
	}
	out := make([]NodeFilesystem, 0, len(current))
	for _, fs := range current {
		out = append(out, *fs)
	}
	return out, nil
}

// trends reads the daily CPU busy and memory used of the nodes.
func (s *Service) trends(ctx context.Context, scope tenant.Scope, nodes []Node) ([]NodeTrend, error) {
	now := s.now()
	from := now.Add(-TrendRange).Truncate(TrendStep)
	read := func(metric string, labels metrics.Labels, value func(float64) float64) (map[identity.NodeID][]DailyPoint, error) {
		series, err := s.reader.Query(ctx, scope, metrics.Query{Metric: metric, Nodes: ids(nodes), Labels: labels, From: from, To: now, Step: TrendStep})
		if err != nil {
			return nil, fmt.Errorf("trend of %s: %w", metric, err)
		}
		out := map[identity.NodeID][]DailyPoint{}
		for _, one := range series {
			for _, p := range one.Points {
				out[one.Node] = append(out[one.Node], DailyPoint{Day: p.Time, Value: value(p.Avg)})
			}
		}
		return out, nil
	}
	cpu, err := read("sermon_cpu_utilization_ratio", metrics.Labels{"mode": "idle"}, func(v float64) float64 { return min(max(1-v/1e6, 0), 1) })
	if err != nil {
		return nil, err
	}
	memory, err := read("sermon_memory_utilization_ratio", nil, func(v float64) float64 { return v / 1e6 })
	if err != nil {
		return nil, err
	}
	var out []NodeTrend
	for _, n := range nodes {
		if len(cpu[n.ID]) == 0 && len(memory[n.ID]) == 0 {
			continue
		}
		out = append(out, NodeTrend{Node: n.ID, Hostname: n.Hostname, Group: n.GroupName, CPU: cpu[n.ID], Memory: memory[n.ID]})
	}
	return out, nil
}
