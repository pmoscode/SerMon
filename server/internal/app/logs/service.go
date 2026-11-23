// Package logs reads the logs of the nodes a caller may see them of (KONZEPT §10.4, M3-08):
// typed filters, a histogram per severity and the context around an entry. Users never write
// LogQL; the queries name only visible nodes and run in the caller's tenant.
package logs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Bounds of a query.
const (
	MaxRange     = 14 * 24 * time.Hour // the retention (D12)
	MaxLimit     = 1000
	DefaultLimit = 200
	MaxNodes     = 100
	// ContextLines are the lines before and after an entry.
	ContextLines = 50
	// HistogramSteps is about the number of buckets of a histogram.
	HistogramSteps = 100
)

// Errors of the service.
var (
	// ErrInvalid marks a filter the service refuses.
	ErrInvalid = errors.New("invalid log query")
	// ErrUnavailable: the log store is not configured or not reachable.
	ErrUnavailable = errors.New("logs are unavailable")
)

// StoredLine is a line as the store returns it.
type StoredLine struct {
	Labels   map[string]string
	At       time.Time
	Line     string
	Metadata map[string]string
}

// CountPoint is one bucket of a count series.
type CountPoint struct {
	At    time.Time
	Value float64
}

// CountSeries are the counts of one label set.
type CountSeries struct {
	Labels map[string]string
	Points []CountPoint
}

// Store reads the log store of an organization (Loki); unreachable is ErrUnavailable. Lines come
// in the asked order over all streams.
type Store interface {
	Lines(ctx context.Context, org tenant.OrgID, q logql.Query, from, to time.Time, limit int, forward bool) ([]StoredLine, error)
	Counts(ctx context.Context, org tenant.OrgID, q logql.Query, from, to time.Time, step time.Duration) ([]CountSeries, error)
}

// Directory lists the nodes of a scope (fleet.Directory).
type Directory interface {
	List(ctx context.Context, scope tenant.Scope) ([]fleet.Node, error)
}

// Filter selects log lines; Nodes and Group narrow the visible nodes.
type Filter struct {
	From, To    time.Time
	Nodes       []identity.NodeID
	Group       *tenant.GroupID
	Sources     []logql.Source
	Units       []string
	MinSeverity logql.Severity // this and more severe; empty: all
	Text        string
	IgnoreCase  bool
}

// Line is a log line for the caller.
type Line struct {
	Node      identity.NodeID
	Hostname  string
	At        time.Time
	Severity  logql.Severity
	Source    logql.Source
	Unit      string
	Message   string
	Truncated bool
	// Fields are the further fields of the entry (identifier, pid, process, …), from the host.
	Fields map[string]string
}

// Page is the result of a query; More says that the range holds more lines.
type Page struct {
	Lines []Line
	More  bool
}

// Histogram counts the lines per severity and step.
type Histogram struct {
	Step   time.Duration
	Series map[logql.Severity][]CountPoint
}

// Service is the use case.
type Service struct {
	store     Store
	directory Directory
	policy    authz.Policy
	now       func() time.Time
	// tailInterval and tailRecheck are TailInterval and TailRecheck outside of tests.
	tailInterval, tailRecheck time.Duration
}

// Option configures the service.
type Option func(*Service)

// WithClock sets the clock (tests).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithTailTiming sets the interval of the tail's rounds and of its rechecks (tests).
func WithTailTiming(interval, recheck time.Duration) Option {
	return func(s *Service) { s.tailInterval, s.tailRecheck = interval, recheck }
}

// NewService returns the service; without a store every query is ErrUnavailable.
func NewService(store Store, directory Directory, policy authz.Policy, opts ...Option) *Service {
	s := &Service{store: store, directory: directory, policy: policy, now: time.Now, tailInterval: TailInterval, tailRecheck: TailRecheck}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Query returns the lines of the filter, newest first unless oldestFirst; 0 is DefaultLimit.
func (s *Service) Query(ctx context.Context, c access.Caller, f Filter, limit int, oldestFirst bool) (Page, error) {
	if limit < 0 || limit > MaxLimit {
		return Page{}, invalid("the limit is at most %d", MaxLimit)
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	if err := checkRange(f); err != nil {
		return Page{}, err
	}
	q, org, hosts, err := s.prepare(ctx, c, f)
	if err != nil || hosts == nil {
		return Page{}, err
	}
	stored, err := s.store.Lines(ctx, org, q.Query, f.From, f.To, limit+1, oldestFirst)
	if err != nil {
		return Page{}, fmt.Errorf("query logs: %w", err)
	}
	page := Page{More: len(stored) > limit}
	for _, l := range stored[:min(len(stored), limit)] {
		page.Lines = append(page.Lines, toLine(l, hosts))
	}
	return page, nil
}

// Histogram counts the lines of the filter per severity in about HistogramSteps buckets.
func (s *Service) Histogram(ctx context.Context, c access.Caller, f Filter) (Histogram, error) {
	if err := checkRange(f); err != nil {
		return Histogram{}, err
	}
	step := max(f.To.Sub(f.From)/HistogramSteps, time.Second).Truncate(time.Second)
	q, org, hosts, err := s.prepare(ctx, c, f)
	if err != nil || hosts == nil {
		return Histogram{Step: step}, err
	}
	filter, _ := toFilter(f, nil)
	filter.Nodes = q.nodes
	counts, err := logql.CountBySeverity(filter, step)
	if err != nil {
		return Histogram{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	series, err := s.store.Counts(ctx, org, counts, f.From, f.To, step)
	if err != nil {
		return Histogram{}, fmt.Errorf("count logs: %w", err)
	}
	h := Histogram{Step: step, Series: map[logql.Severity][]CountPoint{}}
	for _, cs := range series {
		severity := severityOf(cs.Labels["severity"])
		h.Series[severity] = append(h.Series[severity], cs.Points...)
	}
	return h, nil
}

// Context returns up to ContextLines lines before an entry and the entry and as many after,
// of the same node, source and unit; access.ErrNotFound for a node the caller doesn't see.
func (s *Service) Context(ctx context.Context, c access.Caller, node identity.NodeID, at time.Time, source logql.Source, unit string) ([]Line, error) {
	f := Filter{From: at.Add(-MaxRange / 2), To: at.Add(MaxRange / 2), Nodes: []identity.NodeID{node}, Sources: []logql.Source{source}}
	if unit != "" {
		f.Units = []string{unit}
	}
	q, org, hosts, err := s.prepare(ctx, c, f)
	if err != nil {
		return nil, err
	}
	if hosts == nil {
		return nil, access.ErrNotFound
	}
	before, err := s.store.Lines(ctx, org, q.Query, f.From, at, ContextLines, false)
	if err != nil {
		return nil, fmt.Errorf("log context: %w", err)
	}
	after, err := s.store.Lines(ctx, org, q.Query, at, f.To, ContextLines+1, true)
	if err != nil {
		return nil, fmt.Errorf("log context: %w", err)
	}
	slices.Reverse(before)
	lines := make([]Line, 0, len(before)+len(after))
	for _, l := range append(before, after...) {
		lines = append(lines, toLine(l, hosts))
	}
	return lines, nil
}

// prepared is a query of visible nodes.
type prepared struct {
	logql.Query
	nodes []identity.NodeID
}

// checkRange checks the time range of a query.
func checkRange(f Filter) error {
	switch {
	case f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From):
		return invalid("the time range needs from before to")
	case f.To.Sub(f.From) > MaxRange:
		return invalid("the time range is at most %v", MaxRange)
	}
	return nil
}

// prepare checks the filter, resolves the visible nodes and builds the query. Without a visible
// node, hosts is nil and nothing is to be asked.
func (s *Service) prepare(ctx context.Context, c access.Caller, f Filter) (prepared, tenant.OrgID, map[identity.NodeID]string, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewLogs)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return prepared{}, tenant.OrgID{}, nil, access.ErrForbidden
	}
	if len(f.Nodes) > MaxNodes {
		return prepared{}, tenant.OrgID{}, nil, invalid("at most %d nodes", MaxNodes)
	}
	visible, err := s.directory.List(ctx, scope)
	if err != nil {
		return prepared{}, tenant.OrgID{}, nil, fmt.Errorf("visible nodes: %w", err)
	}
	hosts := map[identity.NodeID]string{}
	var selected []identity.NodeID
	for _, n := range visible {
		if (len(f.Nodes) > 0 && !slices.Contains(f.Nodes, n.ID)) || (f.Group != nil && n.Group != *f.Group) {
			continue
		}
		hosts[n.ID] = n.Hostname
		selected = append(selected, n.ID)
	}
	filter, err := toFilter(f, selected)
	if err != nil {
		return prepared{}, tenant.OrgID{}, nil, err
	}
	if len(selected) == 0 {
		return prepared{}, tenant.OrgID{}, nil, nil
	}
	q, err := logql.Lines(filter)
	if err != nil {
		return prepared{}, tenant.OrgID{}, nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if s.store == nil {
		return prepared{}, tenant.OrgID{}, nil, ErrUnavailable
	}
	return prepared{Query: q, nodes: selected}, scope.OrgID(), hosts, nil
}

// toFilter checks the filter's values with the builder (also without visible nodes, so a bad
// filter is an error either way).
func toFilter(f Filter, nodes []identity.NodeID) (logql.Filter, error) {
	filter := logql.Filter{Nodes: nodes, Sources: f.Sources, Units: f.Units, Text: f.Text, IgnoreCase: f.IgnoreCase}
	if f.MinSeverity != "" {
		i := slices.Index(logql.Severities, f.MinSeverity)
		if i < 0 || f.MinSeverity == logql.SeverityUnknown {
			return logql.Filter{}, invalid("unknown severity %q", f.MinSeverity)
		}
		filter.Severities = logql.Severities[:i+1]
	}
	probe := filter
	probe.Nodes = []identity.NodeID{{}}
	if _, err := logql.Lines(probe); err != nil {
		return logql.Filter{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return filter, nil
}

func severityOf(label string) logql.Severity {
	if s := logql.Severity(label); slices.Contains(logql.Severities, s) {
		return s
	}
	return logql.SeverityUnknown
}

func toLine(l StoredLine, hosts map[identity.NodeID]string) Line {
	id, _ := uuid.Parse(l.Labels["node_id"])
	node := identity.NodeID(id)
	unit := l.Labels["unit"]
	if unit == "-" {
		unit = ""
	}
	fields := map[string]string{}
	for k, v := range l.Metadata {
		if k != "truncated" {
			fields[k] = v
		}
	}
	return Line{
		Node: node, Hostname: hosts[node], At: l.At, Severity: severityOf(l.Labels["severity"]),
		Source: logql.Source(l.Labels["source"]), Unit: unit, Message: l.Line,
		Truncated: l.Metadata["truncated"] == "true", Fields: fields,
	}
}
