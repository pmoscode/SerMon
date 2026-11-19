package metrics

import (
	"slices"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// RingSpan is how long the ring buffer keeps 1 s values (D12: 15 min).
const RingSpan = 15 * time.Minute

type ringKey struct {
	org    tenant.OrgID
	node   identity.NodeID
	metric string
	labels string
}

// ringSeries holds one value per second in slots indexed by the second modulo the capacity.
type ringSeries struct {
	group   tenant.GroupID
	labels  Labels
	seconds []uint32 // Unix second of the slot's value; 0 = empty
	values  []float64
	last    uint32
}

// Ring keeps the last RingSpan of 1 s values of every series in memory (live charts, short alert
// windows). It is safe for concurrent use. Memory: 12 bytes per second and series (≈ 10.8 KB per
// series for 15 min).
type Ring struct {
	mu       sync.RWMutex
	capacity int
	series   map[ringKey]*ringSeries
}

// NewRing returns an empty ring holding RingSpan.
func NewRing() *Ring {
	return &Ring{capacity: int(RingSpan / time.Second), series: map[ringKey]*ringSeries{}}
}

// Add stores the value of a series for the second of `at`; a later value for the same second
// replaces the earlier one. The group is the node's group at ingest time (for the scope check).
func (r *Ring) Add(org tenant.OrgID, group tenant.GroupID, node identity.NodeID, metric string, labels Labels, at time.Time, value float64) {
	second := unixSecond(at)
	if second == 0 {
		return
	}
	key := ringKey{org: org, node: node, metric: metric, labels: labels.Key()}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.series[key]
	if !ok {
		s = &ringSeries{labels: labels, seconds: make([]uint32, r.capacity), values: make([]float64, r.capacity)}
		r.series[key] = s
	}
	s.group = group
	slot := int(second % uint32(r.capacity)) //nolint:gosec // G115: capacity is 900
	s.seconds[slot] = second
	s.values[slot] = value
	s.last = max(s.last, second)
}

// Prune forgets series without a value in the last RingSpan before now.
func (r *Ring) Prune(now time.Time) {
	cutoff := unixSecond(now.Add(-RingSpan))
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, s := range r.series {
		if s.last < cutoff {
			delete(r.series, key)
		}
	}
}

// Len returns the number of series held.
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.series)
}

// Query returns the points of the query from the ring, bucketed by q.Step (at least 1 s), for
// series within the scope (organization and node group). Only the last RingSpan is available.
func (r *Ring) Query(scope tenant.Scope, q Query) []Series {
	if !scope.Valid() {
		return nil
	}
	step := max(q.Step, time.Second)
	from, to := q.From.Truncate(time.Second), q.To
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Series
	for key, s := range r.series {
		if key.org != scope.OrgID() || key.metric != q.Metric || !scope.AllowsGroup(s.group) ||
			!slices.Contains(q.Nodes, key.node) || !containsLabels(s.labels, q.Labels) {
			continue
		}
		if points := s.points(from, to, step); len(points) > 0 {
			out = append(out, Series{Node: key.node, Labels: s.labels, Points: points})
		}
	}
	slices.SortFunc(out, func(a, b Series) int {
		if c := slices.Compare(a.Node[:], b.Node[:]); c != 0 {
			return c
		}
		return compareText(a.Labels.Key(), b.Labels.Key())
	})
	return out
}

func (s *ringSeries) points(from, to time.Time, step time.Duration) []Point {
	var points []Point
	var current *Point
	var sum float64
	var count int
	flush := func() {
		if current != nil {
			current.Avg = sum / float64(count)
			points = append(points, *current)
		}
	}
	for at := from; at.Before(to); at = at.Add(time.Second) {
		second := unixSecond(at)
		slot := int(second % uint32(len(s.seconds))) //nolint:gosec // G115: capacity is 900
		if second == 0 || s.seconds[slot] != second {
			continue
		}
		value := s.values[slot]
		bucket := at.Truncate(step)
		if current == nil || !current.Time.Equal(bucket) {
			flush()
			current = &Point{Time: bucket, Min: value, Max: value}
			sum, count = 0, 0
		}
		sum += value
		count++
		current.Min = min(current.Min, value)
		current.Max = max(current.Max, value)
	}
	flush()
	return points
}

func unixSecond(at time.Time) uint32 {
	seconds := at.Unix()
	if seconds <= 0 || seconds > 1<<32-1 {
		return 0
	}
	return uint32(seconds)
}

func containsLabels(labels, filter Labels) bool {
	for key, value := range filter {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func compareText(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
