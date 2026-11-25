// Package memstore is an in-memory metrics.Store for tests of the use cases (M1-24). It passes the
// same contract tests as the TimescaleDB store.
package memstore

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ErrUnknownNode means the node doesn't belong to the organization.
var ErrUnknownNode = errors.New("node not found in the organization")

type node struct {
	org   tenant.OrgID
	group tenant.GroupID
	seq   uint64
}

// Store keeps everything in memory; safe for concurrent use.
type Store struct {
	mu         sync.Mutex
	nodes      map[identity.NodeID]*node
	aggregates []metrics.Aggregate
	writes     int
}

var _ metrics.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store { return &Store{nodes: map[identity.NodeID]*node{}} }

// AddNode registers a node of org in group.
func (s *Store) AddNode(org tenant.OrgID, group tenant.GroupID, id identity.NodeID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[id] = &node{org: org, group: group}
}

// Aggregates returns everything written so far.
func (s *Store) Aggregates() []metrics.Aggregate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.aggregates)
}

// Writes returns the number of successful Write calls.
func (s *Store) Writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// Write implements metrics.Store: all or nothing.
func (s *Store) Write(_ context.Context, org tenant.OrgID, aggregates []metrics.Aggregate, persisted map[identity.NodeID]uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range aggregates {
		if n, ok := s.nodes[a.Node]; !ok || n.org != org {
			return ErrUnknownNode
		}
	}
	for id := range persisted {
		if n, ok := s.nodes[id]; !ok || n.org != org {
			return ErrUnknownNode
		}
	}
	s.aggregates = append(s.aggregates, aggregates...)
	for id, seq := range persisted {
		s.nodes[id].seq = max(s.nodes[id].seq, seq)
	}
	s.writes++
	return nil
}

// NodeSeries implements metrics.Store.
func (s *Store) NodeSeries(_ context.Context, org tenant.OrgID, id identity.NodeID, since time.Time) ([]metrics.SeriesKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; !ok || n.org != org {
		return nil, nil
	}
	var out []metrics.SeriesKey
	for _, e := range s.lastSeen(func(a metrics.Aggregate) bool { return a.Node == id }) {
		if !e.seen.Before(since) {
			out = append(out, e.key)
		}
	}
	return out, nil
}

// OrgSeriesCount implements metrics.Store.
func (s *Store) OrgSeriesCount(_ context.Context, org tenant.OrgID, since time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	inOrg := func(a metrics.Aggregate) bool { n, ok := s.nodes[a.Node]; return ok && n.org == org }
	for _, e := range s.lastSeen(inOrg) {
		if !e.seen.Before(since) {
			count++
		}
	}
	return count, nil
}

type seenSeries struct {
	key  metrics.SeriesKey
	seen time.Time
}

// lastSeen returns the newest window start per series (node, metric, labels) of the matching
// aggregates; s.mu is held.
func (s *Store) lastSeen(match func(metrics.Aggregate) bool) map[string]*seenSeries {
	out := map[string]*seenSeries{}
	for _, a := range s.aggregates {
		if !match(a) {
			continue
		}
		id := a.Node.String() + "\x1f" + a.Metric + "\x1f" + a.Labels.Key()
		e, ok := out[id]
		if !ok {
			e = &seenSeries{key: metrics.SeriesKey{Metric: a.Metric, Labels: a.Labels}}
			out[id] = e
		}
		if a.Start.After(e.seen) {
			e.seen = a.Start
		}
	}
	return out
}

// PersistedSeq implements metrics.Store.
func (s *Store) PersistedSeq(_ context.Context, org tenant.OrgID, id identity.NodeID) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || n.org != org {
		return 0, ErrUnknownNode
	}
	return n.seq, nil
}

type bucketKey struct {
	node   identity.NodeID
	labels string
	at     time.Time
}

type bucket struct {
	sum, min, max float64
	count         int
}

// Query implements metrics.Store. The 1 min and 1 h sources are computed from the 10 s windows.
func (s *Store) Query(_ context.Context, scope tenant.Scope, q metrics.Query) ([]metrics.Series, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !scope.Valid() {
		return nil, metrics.ErrInvalidQuery
	}
	step := max(q.Step, q.Source.Resolution())
	buckets := map[bucketKey]*bucket{}
	labelsOf := map[string]metrics.Labels{}
	for _, a := range s.aggregates {
		n := s.nodes[a.Node]
		visible := n.org == scope.OrgID() && scope.AllowsGroup(n.group)
		if !visible || a.Metric != q.Metric || !slices.Contains(q.Nodes, a.Node) || !contains(a.Labels, q.Labels) ||
			a.Start.Before(q.From) || !a.Start.Before(q.To) {
			continue
		}
		key := bucketKey{node: a.Node, labels: a.Labels.Key(), at: a.Start.Truncate(step)}
		labelsOf[key.labels] = a.Labels
		b, ok := buckets[key]
		if !ok {
			b = &bucket{min: a.Min, max: a.Max}
			buckets[key] = b
		}
		b.sum += a.Avg
		b.count++
		b.min = min(b.min, a.Min)
		b.max = max(b.max, a.Max)
	}
	type seriesKey struct {
		node   identity.NodeID
		labels string
	}
	bySeries := map[seriesKey]*metrics.Series{}
	var order []seriesKey
	for key, b := range buckets {
		sk := seriesKey{key.node, key.labels}
		series, ok := bySeries[sk]
		if !ok {
			series = &metrics.Series{Node: key.node, Labels: labelsOf[key.labels]}
			bySeries[sk] = series
			order = append(order, sk)
		}
		series.Points = append(series.Points, metrics.Point{Time: key.at, Avg: b.sum / float64(b.count), Min: b.min, Max: b.max})
	}
	slices.SortFunc(order, func(a, b seriesKey) int {
		if c := slices.Compare(a.node[:], b.node[:]); c != 0 {
			return c
		}
		return compareStrings(a.labels, b.labels)
	})
	// At most MaxSeries, like the TimescaleDB store (M4-27: callers must stay within it).
	if len(order) > metrics.MaxSeries {
		order = order[:metrics.MaxSeries]
	}
	out := make([]metrics.Series, 0, len(order))
	for _, key := range order {
		series := bySeries[key]
		slices.SortFunc(series.Points, func(a, b metrics.Point) int { return a.Time.Compare(b.Time) })
		out = append(out, *series)
	}
	return out, nil
}

func contains(labels, filter metrics.Labels) bool {
	for key, value := range filter {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
