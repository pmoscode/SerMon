package anomalies

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Limits of the readers (ADR 0037).
const (
	// MaxRange is the longest range asked for; Retention how far back the results go.
	MaxRange  = 7 * 24 * time.Hour
	Retention = 30 * 24 * time.Hour
	// MaxNodeSeries and MaxFleetSeries bound the series of one request; MaxMinutes its minutes.
	MaxNodeSeries  = 256
	MaxFleetSeries = 20000
	MaxMinutes     = 20000
	// MaxUnusual is the longest "what was unusual?" list.
	MaxUnusual = 50
)

// ErrInvalidRange means the range is empty, too long or older than the results.
var ErrInvalidRange = errors.New("the range must be at most 7 days within the last 30 days")

// SeriesRef is a candidate series a reader may see.
type SeriesRef struct {
	ID     int64
	Node   identity.NodeID
	Metric string
	Labels map[string]string
}

// Top is a series' highest score in a range.
type Top struct {
	Series    int64
	MaxScore  float64
	Peak      time.Time
	Anomalous int
}

// Reader reads the results within a scope (the readers' port to the database).
type Reader interface {
	// Learning returns since when a node of the scope learns; false if it is not visible.
	Learning(ctx context.Context, scope tenant.Scope, node identity.NodeID) (time.Time, bool, error)
	// ScopedSeries returns the candidate series of the scope's nodes (nodes empty: all), at most
	// limit.
	ScopedSeries(ctx context.Context, scope tenant.Scope, metrics []string, nodes []identity.NodeID, limit int) ([]SeriesRef, error)
	// Minutes returns the notable minutes of the series in [from, to), at most limit.
	Minutes(ctx context.Context, scope tenant.Scope, ids []int64, from, to time.Time, limit int) ([]Notable, error)
	// Top returns the series with the highest scores in [from, to), at most limit.
	Top(ctx context.Context, scope tenant.Scope, ids []int64, from, to time.Time, limit int) ([]Top, error)
}

// Service is the readers' use cases.
type Service struct {
	reader Reader
	policy authz.Policy
	now    func() time.Time
	names  []string
}

// NewService returns the service.
func NewService(reader Reader, policy authz.Policy, now func() time.Time) *Service {
	s := &Service{reader: reader, policy: policy, now: now}
	for _, d := range metric.Candidates() {
		s.names = append(s.names, d.Name)
	}
	return s
}

func (s *Service) scope(c access.Caller) (tenant.Scope, error) {
	scope, err := s.policy.ScopeForAction(c.Principal, authz.ViewNodes)
	if err != nil || (!scope.AllGroups() && len(scope.Groups()) == 0) {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

func (s *Service) checkRange(from, to time.Time) error {
	now := s.now()
	if !from.Before(to) || to.Sub(from) > MaxRange || from.Before(now.Add(-Retention)) {
		return ErrInvalidRange
	}
	return nil
}

// Interval is a stretch of anomalous minutes [From, To).
type Interval struct {
	From, To time.Time
}

// SeriesAnomalies are the anomalous stretches of a series and its highest score.
type SeriesAnomalies struct {
	SeriesRef
	MaxScore  float64
	Intervals []Interval
}

// NodeAnomalies is the band of a node's charts.
type NodeAnomalies struct {
	// LearningSince is when the node began to learn; LearningDay is the day of the learning phase
	// (1 to 7), 0 when it is over.
	LearningSince time.Time
	LearningDay   int
	Series        []SeriesAnomalies
}

// Node returns the anomalous stretches of a node's series in [from, to): access.ErrNotFound for
// a node outside the scope.
func (s *Service) Node(ctx context.Context, c access.Caller, node identity.NodeID, from, to time.Time) (NodeAnomalies, error) {
	scope, err := s.scope(c)
	if err != nil {
		return NodeAnomalies{}, err
	}
	if err := s.checkRange(from, to); err != nil {
		return NodeAnomalies{}, err
	}
	since, ok, err := s.reader.Learning(ctx, scope, node)
	if err != nil {
		return NodeAnomalies{}, fmt.Errorf("read node: %w", err)
	}
	if !ok {
		return NodeAnomalies{}, access.ErrNotFound
	}
	out := NodeAnomalies{LearningSince: since}
	if age := s.now().Sub(since); age < Learning {
		out.LearningDay = int(age/(24*time.Hour)) + 1
	}
	refs, err := s.reader.ScopedSeries(ctx, scope, s.names, []identity.NodeID{node}, MaxNodeSeries)
	if err != nil {
		return NodeAnomalies{}, fmt.Errorf("read series: %w", err)
	}
	if len(refs) == 0 {
		return out, nil
	}
	ids := make([]int64, len(refs))
	byID := map[int64]*SeriesAnomalies{}
	for i, r := range refs {
		ids[i] = r.ID
		byID[r.ID] = &SeriesAnomalies{SeriesRef: r}
	}
	minutes, err := s.reader.Minutes(ctx, scope, ids, from, to, MaxMinutes)
	if err != nil {
		return NodeAnomalies{}, fmt.Errorf("read anomalies: %w", err)
	}
	for _, m := range minutes {
		sa := byID[m.Series]
		if sa == nil {
			continue
		}
		sa.MaxScore = max(sa.MaxScore, m.Score)
		if !m.Anomalous {
			continue
		}
		if n := len(sa.Intervals); n > 0 && !sa.Intervals[n-1].To.Before(m.T) {
			sa.Intervals[n-1].To = m.T.Add(time.Minute)
		} else {
			sa.Intervals = append(sa.Intervals, Interval{From: m.T, To: m.T.Add(time.Minute)})
		}
	}
	for _, r := range refs {
		if sa := byID[r.ID]; sa.MaxScore > 0 {
			out.Series = append(out.Series, *sa)
		}
	}
	return out, nil
}

// Unusual is a series of "what was unusual?".
type Unusual struct {
	SeriesRef
	Top
}

// Unusual returns the series of a node (or of all nodes of the scope, node nil) with the highest
// scores in [from, to), at most limit (1 to MaxUnusual).
func (s *Service) Unusual(ctx context.Context, c access.Caller, node *identity.NodeID, from, to time.Time, limit int) ([]Unusual, error) {
	scope, err := s.scope(c)
	if err != nil {
		return nil, err
	}
	if err := s.checkRange(from, to); err != nil {
		return nil, err
	}
	limit = min(max(limit, 1), MaxUnusual)
	var nodes []identity.NodeID
	if node != nil {
		if _, ok, err := s.reader.Learning(ctx, scope, *node); err != nil {
			return nil, fmt.Errorf("read node: %w", err)
		} else if !ok {
			return nil, access.ErrNotFound
		}
		nodes = []identity.NodeID{*node}
	}
	refs, err := s.reader.ScopedSeries(ctx, scope, s.names, nodes, MaxFleetSeries)
	if err != nil {
		return nil, fmt.Errorf("read series: %w", err)
	}
	if len(refs) == 0 {
		return []Unusual{}, nil
	}
	ids := make([]int64, len(refs))
	byID := map[int64]SeriesRef{}
	for i, r := range refs {
		ids[i], byID[r.ID] = r.ID, r
	}
	tops, err := s.reader.Top(ctx, scope, ids, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("read anomalies: %w", err)
	}
	out := make([]Unusual, 0, len(tops))
	for _, t := range tops {
		if r, ok := byID[t.Series]; ok {
			out = append(out, Unusual{SeriesRef: r, Top: t})
		}
	}
	slices.SortStableFunc(out, func(a, b Unusual) int {
		switch {
		case a.MaxScore > b.MaxScore:
			return -1
		case a.MaxScore < b.MaxScore:
			return 1
		}
		return 0
	})
	return out, nil
}
