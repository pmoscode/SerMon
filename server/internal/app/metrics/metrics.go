// Package metrics is the metric use-case layer (KONZEPT §4.5, §8, ADR 0001): the MetricStore
// port, the choice of the storage resolution and the query limits.
//
// Retention (D12): 1 s in the server's ring buffer for 15 min, 10 s windows for 24 h, 1 min
// aggregates for 30 days, 1 h aggregates for a year.
package metrics

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Window is the resolution of the database (spike M0-09: 10 s).
const Window = 10 * time.Second

// Limits of one query.
const (
	// MaxPointsPerSeries bounds the points returned per series (charts are at most a few
	// thousand pixels wide).
	MaxPointsPerSeries = 2000
	// MaxSeries bounds the series of one query.
	MaxSeries = 500
	// MaxNodes bounds the nodes of one query.
	MaxNodes = 500
)

var (
	// ErrInvalidQuery means the query is malformed (no metric, no nodes, empty range).
	ErrInvalidQuery = errors.New("invalid metric query")
	// ErrTooManyPoints means the range is too long for the step.
	ErrTooManyPoints = errors.New("metric query returns too many points: choose a larger step")
)

// Labels are the technical labels of a series, e.g. {"cpu": "0", "mode": "user"}.
type Labels map[string]string

// Key is the canonical text of the labels (sorted), for caches and maps.
func (l Labels) Key() string {
	keys := slices.Sorted(maps.Keys(l))
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(l[key])
		b.WriteByte(0x1f)
	}
	return b.String()
}

// Aggregate is one 10 s window of one series, written by the ingest.
type Aggregate struct {
	Node   identity.NodeID
	Metric string
	Labels Labels
	// Start of the window (aligned to Window).
	Start         time.Time
	Avg, Min, Max float64
}

// Source is a storage resolution.
type Source int

// Storage resolutions, finest first.
const (
	SourceRaw  Source = iota + 1 // 10 s windows, 24 h
	Source1m                     // 1 min aggregates, 30 days
	Source1h                     // 1 h aggregates, 1 year
	SourceRing                   // 1 s values in the server's memory, 15 min (not in the Store)
)

// Resolution of a source.
func (s Source) Resolution() time.Duration {
	switch s {
	case SourceRaw:
		return Window
	case Source1m:
		return time.Minute
	case Source1h:
		return time.Hour
	case SourceRing:
		return time.Second
	default:
		return time.Hour
	}
}

// retention of a source (D12).
func (s Source) retention() time.Duration {
	switch s {
	case SourceRaw:
		return 24 * time.Hour
	case Source1m:
		return 30 * 24 * time.Hour
	case Source1h:
		return 365 * 24 * time.Hour
	case SourceRing:
		return RingSpan
	default:
		return 365 * 24 * time.Hour
	}
}

// ChooseSource picks the finest source that still holds `from` and whose resolution is not finer
// than the step (a 5 min step needs no 10 s windows): ring → raw → 1 min → 1 h.
func ChooseSource(from time.Time, step time.Duration, now time.Time) Source {
	if !from.Before(now.Add(-RingSpan)) && step < Window {
		return SourceRing
	}
	for _, source := range []Source{SourceRaw, Source1m} {
		next := source + 1
		holdsRange := !from.Before(now.Add(-source.retention()))
		if holdsRange && step < next.Resolution() {
			return source
		}
	}
	return Source1h
}

// Query asks for the points of one metric.
type Query struct {
	Metric string
	Nodes  []identity.NodeID
	// Labels filter: only series whose labels contain all of these.
	Labels Labels
	// From (inclusive) and To (exclusive).
	From, To time.Time
	// Step is the bucket size of the points; it is aligned to the source's resolution. Zero picks
	// the finest step within MaxPointsPerSeries.
	Step time.Duration
	// Source; zero chooses one with ChooseSource.
	Source Source
}

// Normalize validates the query, chooses the source and aligns the step; now is the server time.
func (q Query) Normalize(now time.Time) (Query, error) {
	if q.Metric == "" || len(q.Nodes) == 0 || len(q.Nodes) > MaxNodes || !q.From.Before(q.To) || q.Step < 0 {
		return Query{}, ErrInvalidQuery
	}
	auto := q.Step == 0
	if auto {
		span := q.To.Sub(q.From)
		q.Step = (span + MaxPointsPerSeries - 1) / MaxPointsPerSeries
	}
	if q.Source == 0 {
		q.Source = ChooseSource(q.From, q.Step, now)
	}
	resolution := q.Source.Resolution()
	if auto {
		q.Step = (q.Step + resolution - 1) / resolution * resolution
	}
	q.Step = max(q.Step, resolution).Truncate(resolution)
	if q.To.Sub(q.From)/q.Step > MaxPointsPerSeries {
		return Query{}, ErrTooManyPoints
	}
	return q, nil
}

// Point is one bucket of a series.
type Point struct {
	Time          time.Time
	Avg, Min, Max float64
}

// Series are the points of one series of the query.
type Series struct {
	Node   identity.NodeID
	Labels Labels
	Points []Point
}

// Store persists aggregates and answers queries. Implementations must pass the contract tests in
// package metricstoretest, including tenant isolation.
type Store interface {
	// Write stores aggregates of nodes of one organization and records, per node, the highest
	// batch sequence number they contain – in one transaction, so that a batch is either stored
	// and counted or neither (deduplication, KONZEPT §5). The organization comes from the agent
	// certificate; nodes of other organizations are rejected.
	Write(ctx context.Context, org tenant.OrgID, aggregates []Aggregate, persisted map[identity.NodeID]uint64) error
	// PersistedSeq returns the highest batch sequence number stored for the node.
	PersistedSeq(ctx context.Context, org tenant.OrgID, node identity.NodeID) (uint64, error)
	// Query returns the series of a normalized query within the scope (organization and node
	// groups); series outside the scope are never returned.
	Query(ctx context.Context, scope tenant.Scope, q Query) ([]Series, error)
}
