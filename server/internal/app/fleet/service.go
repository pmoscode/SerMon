// Package fleet is the read side for the UI (M1-26): fleet overview, node details, file systems
// with fill forecast, metric queries and live values. Every call is limited to the node groups in
// which the caller may view nodes (authz ViewNodes, KONZEPT §7).
package fleet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Time windows of the live values and the forecast.
const (
	// liveWindow is how far back the current values may be (file systems report every 10 s).
	liveWindow = time.Minute
	// forecastRange and forecastStep are the history for the fill forecast (KONZEPT §8.2).
	forecastRange = 24 * time.Hour
	forecastStep  = 10 * time.Minute
	// forecastHorizon is the longest forecast shown.
	forecastHorizon = 365 * 24 * time.Hour
	// WatchDuration ends a live stream; the client opens a new one.
	WatchDuration = 15 * time.Minute
	// WatchInterval is how often a live stream sends new points.
	WatchInterval = time.Second
	// watchLookback covers the delay between a sample's second and its arrival.
	watchLookback = 5 * time.Second
)

// Node is a node record as the UI sees it.
type Node struct {
	ID            identity.NodeID
	Group         tenant.GroupID
	GroupName     string
	Hostname      string
	OSID          string
	OSVersion     string
	KernelRelease string
	Architecture  string
	CPUCount      uint32
	MemoryBytes   uint64
	AgentVersion  string
	EnrolledAt    time.Time
	LastSeenAt    *time.Time
	// SecuritySource is where its agent reads security events, as of the last Hello (M2-31).
	SecuritySource secevent.Source
	// LogsVetoed: its agent.toml forbids sending logs (allow_logs = false), as of its last config report.
	LogsVetoed bool
	// Identity (M2-23): a lock after a second session, the address of the last session and an
	// open takeover window.
	LockedAt      *time.Time
	LockReason    string
	LastIP        string
	TakeoverUntil *time.Time
	// Tags are the free key=value pairs admins set (M2-29).
	Tags tag.Tags
	// LearningSince is when SerMon began to learn the node's login sources (M3-16): its
	// enrollment, for nodes enrolled before that the upgrade.
	LearningSince time.Time
}

// Directory reads node records within a scope.
type Directory interface {
	// List returns the nodes of the scope's groups, sorted by host name.
	List(ctx context.Context, scope tenant.Scope) ([]Node, error)
	// HasGroup reports whether the group exists within the scope.
	HasGroup(ctx context.Context, scope tenant.Scope, group tenant.GroupID) (bool, error)
}

// Figures are the current key values of a node (nil: no live data).
type Figures struct {
	CPUBusy, MemoryUsed, FilesystemMaxUsed, LoadPerCPU, Uptime, ReceiveBytes, TransmitBytes *float64
}

// NodeStatus is a node with status and figures.
type NodeStatus struct {
	Node
	Online  bool
	Figures Figures
}

// Summary is the fleet overview.
type Summary struct {
	Nodes           []NodeStatus
	Online, Offline int
}

// Filesystem is one mounted file system.
type Filesystem struct {
	Mountpoint              string
	Size, Used, Avail       uint64
	UsedRatio               float64
	Readonly                bool
	InodesTotal, InodesUsed uint64
	FullAt                  *time.Time
}

// Service implements the read side.
type Service struct {
	nodes  Directory
	reader *metrics.Reader
	now    func() time.Time
}

// NewService returns the service.
func NewService(nodes Directory, reader *metrics.Reader, now func() time.Time) *Service {
	return &Service{nodes: nodes, reader: reader, now: now}
}

// visible returns the scope and the nodes the caller may view.
func (s *Service) visible(ctx context.Context, c access.Caller) (tenant.Scope, []Node, error) {
	scope, err := authz.ScopeFor(c.Principal, role.Viewer)
	if err != nil {
		return tenant.Scope{}, nil, access.ErrForbidden
	}
	nodes, err := s.nodes.List(ctx, scope)
	if err != nil {
		return tenant.Scope{}, nil, fmt.Errorf("list nodes: %w", err)
	}
	return scope, nodes, nil
}

// Summary returns the overview, optionally of one group (a foreign group is access.ErrNotFound).
func (s *Service) Summary(ctx context.Context, c access.Caller, group *tenant.GroupID) (Summary, error) {
	scope, nodes, err := s.visible(ctx, c)
	if err != nil {
		return Summary{}, err
	}
	if group != nil {
		exists, err := s.nodes.HasGroup(ctx, scope, *group)
		if err != nil {
			return Summary{}, fmt.Errorf("check group: %w", err)
		}
		if !exists {
			return Summary{}, access.ErrNotFound
		}
		nodes = slices.DeleteFunc(nodes, func(n Node) bool { return n.Group != *group })
	}
	figures, err := s.figures(ctx, scope, nodes)
	if err != nil {
		return Summary{}, err
	}
	now := s.now()
	summary := Summary{Nodes: make([]NodeStatus, 0, len(nodes))}
	for _, n := range nodes {
		online := ingest.Online(n.LastSeenAt, now)
		if online {
			summary.Online++
		} else {
			summary.Offline++
		}
		summary.Nodes = append(summary.Nodes, NodeStatus{Node: n, Online: online, Figures: figures[n.ID]})
	}
	return summary, nil
}

// Node returns one node (access.ErrNotFound if it is not visible).
func (s *Service) Node(ctx context.Context, c access.Caller, id identity.NodeID) (NodeStatus, error) {
	scope, node, err := s.node(ctx, c, id)
	if err != nil {
		return NodeStatus{}, err
	}
	figures, err := s.figures(ctx, scope, []Node{node})
	if err != nil {
		return NodeStatus{}, err
	}
	return NodeStatus{Node: node, Online: ingest.Online(node.LastSeenAt, s.now()), Figures: figures[id]}, nil
}

func (s *Service) node(ctx context.Context, c access.Caller, id identity.NodeID) (tenant.Scope, Node, error) {
	scope, nodes, err := s.visible(ctx, c)
	if err != nil {
		return tenant.Scope{}, Node{}, err
	}
	index := slices.IndexFunc(nodes, func(n Node) bool { return n.ID == id })
	if index < 0 {
		return tenant.Scope{}, Node{}, access.ErrNotFound
	}
	return scope, nodes[index], nil
}

// Query reads metrics of visible nodes; any other node makes it access.ErrNotFound.
func (s *Service) Query(ctx context.Context, c access.Caller, q metrics.Query) ([]metrics.Series, time.Duration, error) {
	scope, err := s.authorizeNodes(ctx, c, q.Nodes)
	if err != nil {
		return nil, 0, err
	}
	normalized, err := q.Normalize(s.now())
	if err != nil {
		return nil, 0, fmt.Errorf("query metrics: %w", err)
	}
	series, err := s.reader.Query(ctx, scope, normalized)
	if err != nil {
		return nil, 0, fmt.Errorf("query metrics: %w", err)
	}
	return series, normalized.Step, nil
}

// Watch sends the new 1 s points of the selected series about every second until ctx ends or
// WatchDuration is over.
func (s *Service) Watch(ctx context.Context, c access.Caller, q metrics.Query, send func([]metrics.Series) error) error {
	scope, err := s.authorizeNodes(ctx, c, q.Nodes)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, WatchDuration)
	defer cancel()
	ticker := time.NewTicker(WatchInterval)
	defer ticker.Stop()
	// Samples arrive a moment after their second: every round looks back watchLookback and sends,
	// per series, only points newer than those already sent.
	sent := map[string]time.Time{}
	for {
		now := s.now()
		live := q
		live.From, live.To, live.Step, live.Source = now.Add(-watchLookback), now.Add(time.Second), time.Second, metrics.SourceRing
		series, err := s.reader.Query(ctx, scope, live)
		if err != nil {
			return fmt.Errorf("watch metrics: %w", err)
		}
		fresh := make([]metrics.Series, 0, len(series))
		for _, one := range series {
			key := one.Node.String() + "\x1f" + one.Labels.Key()
			newer := slices.DeleteFunc(one.Points, func(p metrics.Point) bool { return !p.Time.After(sent[key]) && !sent[key].IsZero() })
			if len(newer) == 0 {
				continue
			}
			sent[key] = newer[len(newer)-1].Time
			one.Points = newer
			fresh = append(fresh, one)
		}
		if err := send(fresh); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return ctx.Err() //nolint:wrapcheck // cancellation by the client
		case <-ticker.C:
		}
	}
}

func (s *Service) authorizeNodes(ctx context.Context, c access.Caller, ids []identity.NodeID) (tenant.Scope, error) {
	scope, nodes, err := s.visible(ctx, c)
	if err != nil {
		return tenant.Scope{}, err
	}
	for _, id := range ids {
		if !slices.ContainsFunc(nodes, func(n Node) bool { return n.ID == id }) {
			return tenant.Scope{}, access.ErrNotFound
		}
	}
	return scope, nil
}

// Filesystems returns the file systems of a visible node with the fill forecast.
func (s *Service) Filesystems(ctx context.Context, c access.Caller, id identity.NodeID) ([]Filesystem, error) {
	scope, _, err := s.node(ctx, c, id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	current := map[string]*Filesystem{}
	get := func(mountpoint string) *Filesystem {
		fs, ok := current[mountpoint]
		if !ok {
			fs = &Filesystem{Mountpoint: mountpoint}
			current[mountpoint] = fs
		}
		return fs
	}
	values := map[string]func(*Filesystem, float64){
		"sermon_filesystem_size_bytes":        func(fs *Filesystem, v float64) { fs.Size = toUint(v) },
		"sermon_filesystem_used_bytes":        func(fs *Filesystem, v float64) { fs.Used = toUint(v) },
		"sermon_filesystem_avail_bytes":       func(fs *Filesystem, v float64) { fs.Avail = toUint(v) },
		"sermon_filesystem_utilization_ratio": func(fs *Filesystem, v float64) { fs.UsedRatio = v / 1e6 },
		"sermon_filesystem_readonly":          func(fs *Filesystem, v float64) { fs.Readonly = v > 0 },
		"sermon_filesystem_inodes_total":      func(fs *Filesystem, v float64) { fs.InodesTotal = toUint(v) },
		"sermon_filesystem_inodes_used":       func(fs *Filesystem, v float64) { fs.InodesUsed = toUint(v) },
	}
	for metric, set := range values {
		series, err := s.latest(ctx, scope, metric, []identity.NodeID{id}, nil)
		if err != nil {
			return nil, err
		}
		for _, one := range series {
			if value, ok := last(one); ok {
				set(get(one.Labels["mountpoint"]), value)
			}
		}
	}
	history, err := s.reader.Query(ctx, scope, metrics.Query{
		Metric: "sermon_filesystem_avail_bytes", Nodes: []identity.NodeID{id},
		From: now.Add(-forecastRange), To: now, Step: forecastStep,
	})
	if err != nil {
		return nil, fmt.Errorf("filesystem history: %w", err)
	}
	for _, one := range history {
		if fs, ok := current[one.Labels["mountpoint"]]; ok {
			if full, ok := FullAt(one.Points, now, forecastHorizon); ok {
				fs.FullAt = &full
			}
		}
	}
	out := make([]Filesystem, 0, len(current))
	for _, fs := range current {
		out = append(out, *fs)
	}
	slices.SortFunc(out, func(a, b Filesystem) int { return strings.Compare(a.Mountpoint, b.Mountpoint) })
	return out, nil
}

// figures reads the current key values of the nodes from the ring buffer.
func (s *Service) figures(ctx context.Context, scope tenant.Scope, nodes []Node) (map[identity.NodeID]Figures, error) {
	out := make(map[identity.NodeID]Figures, len(nodes))
	if len(nodes) == 0 {
		return out, nil
	}
	ids := make([]identity.NodeID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	type figure struct {
		metric  string
		labels  metrics.Labels
		combine func(values []float64) float64
		set     func(f *Figures, v float64)
	}
	single := func(values []float64) float64 { return values[0] }
	sum := func(values []float64) float64 {
		total := 0.0
		for _, v := range values {
			total += v
		}
		return total
	}
	figures := []figure{
		{"sermon_cpu_utilization_ratio", metrics.Labels{"mode": "idle"}, single, func(f *Figures, v float64) { f.CPUBusy = ptr(1 - v/1e6) }},
		{"sermon_memory_utilization_ratio", nil, single, func(f *Figures, v float64) { f.MemoryUsed = ptr(v / 1e6) }},
		{"sermon_filesystem_utilization_ratio", nil, slices.Max[[]float64], func(f *Figures, v float64) { f.FilesystemMaxUsed = ptr(v / 1e6) }},
		{"sermon_load_average_per_cpu", metrics.Labels{"period": "1m"}, single, func(f *Figures, v float64) { f.LoadPerCPU = ptr(v) }},
		{"sermon_system_uptime_seconds", nil, single, func(f *Figures, v float64) { f.Uptime = ptr(v) }},
		{"sermon_network_receive_bytes_per_second", nil, sum, func(f *Figures, v float64) { f.ReceiveBytes = ptr(v) }},
		{"sermon_network_transmit_bytes_per_second", nil, sum, func(f *Figures, v float64) { f.TransmitBytes = ptr(v) }},
	}
	for _, fig := range figures {
		series, err := s.latest(ctx, scope, fig.metric, ids, fig.labels)
		if err != nil {
			return nil, err
		}
		values := map[identity.NodeID][]float64{}
		for _, one := range series {
			if value, ok := last(one); ok {
				values[one.Node] = append(values[one.Node], value)
			}
		}
		for node, vs := range values {
			f := out[node]
			fig.set(&f, fig.combine(vs))
			out[node] = f
		}
	}
	return out, nil
}

// latest queries the last minute of a metric from the ring buffer.
func (s *Service) latest(ctx context.Context, scope tenant.Scope, metric string, nodes []identity.NodeID, labels metrics.Labels) ([]metrics.Series, error) {
	now := s.now()
	series, err := s.reader.Query(ctx, scope, metrics.Query{
		Metric: metric, Nodes: nodes, Labels: labels, From: now.Add(-liveWindow), To: now.Add(time.Second),
		Step: time.Second, Source: metrics.SourceRing,
	})
	if err != nil {
		return nil, fmt.Errorf("current %s: %w", metric, err)
	}
	return series, nil
}

func last(series metrics.Series) (float64, bool) {
	if len(series.Points) == 0 {
		return 0, false
	}
	return series.Points[len(series.Points)-1].Avg, true
}

func ptr(v float64) *float64 { return &v }

func toUint(v float64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}
