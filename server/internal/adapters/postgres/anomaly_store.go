package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/domain/anomaly"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// AnomalyStore is the anomaly detection's storage (M4-23, ADR 0037): the worker's port and the
// readers'. The results are read only through the SECURITY DEFINER functions (O8).
type AnomalyStore struct {
	db     *DB
	alerts *AlertStore
}

var (
	_ anomalies.Store  = (*AnomalyStore)(nil)
	_ anomalies.Reader = (*AnomalyStore)(nil)
)

// NewAnomalyStore returns the store.
func NewAnomalyStore(db *DB) *AnomalyStore { return &AnomalyStore{db: db, alerts: NewAlertStore(db)} }

// Organizations implements anomalies.Store.
func (s *AnomalyStore) Organizations(ctx context.Context) ([]tenant.OrgID, error) {
	return s.alerts.Organizations(ctx)
}

// Lease implements anomalies.Store.
func (s *AnomalyStore) Lease(ctx context.Context, org tenant.OrgID, holder string, now, until time.Time) (bool, error) {
	scope, err := orgScope(org)
	if err != nil {
		return false, err
	}
	var n int64
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, leaseErr := q.LeaseAnomalyOrganization(ctx, sqlcdb.LeaseAnomalyOrganizationParams{Holder: holder, Until: until, Now: now})
		n = rows
		return wrap(leaseErr, "lease anomalies")
	})
	return n == 1, err
}

func labelsOf(raw []byte) (map[string]string, error) {
	labels := map[string]string{}
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, fmt.Errorf("series labels: %w", err)
	}
	return labels, nil
}

// Series implements anomalies.Store.
func (s *AnomalyStore) Series(ctx context.Context, org tenant.OrgID, metrics []string, since time.Time, limit int) ([]anomalies.Series, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	var out []anomalies.Series
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.AnomalyCandidateSeries(ctx, sqlcdb.AnomalyCandidateSeriesParams{Metrics: metrics, Since: since, MaxSeries: int32(min(limit, 1<<30))}) //nolint:gosec // bounded
		if err != nil {
			return wrap(err, "list anomaly series")
		}
		out = make([]anomalies.Series, 0, len(rows))
		for _, r := range rows {
			labels, err := labelsOf(r.Labels)
			if err != nil {
				return err
			}
			out = append(out, anomalies.Series{
				ID: r.ID, Node: identity.NodeID(r.NodeID), Metric: r.Metric, Labels: labels, Created: r.CreatedAt, Learning: r.LearningSince,
			})
		}
		return nil
	})
	return out, err
}

var oneMinute = pgtype.Interval{Microseconds: time.Minute.Microseconds(), Valid: true}

// Points implements anomalies.Store: the 1-min averages.
func (s *AnomalyStore) Points(ctx context.Context, org tenant.OrgID, ids []int64, from, to time.Time) ([]anomalies.Point, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	var out []anomalies.Point
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.Points1m(ctx, sqlcdb.Points1mParams{SeriesIds: ids, FromTs: from, ToTs: to, Step: oneMinute})
		if err != nil {
			return wrap(err, "read 1-min points")
		}
		out = make([]anomalies.Point, len(rows))
		for i, r := range rows {
			out[i] = anomalies.Point{Series: r.SeriesID, T: r.Bucket.UTC(), V: r.Avg}
		}
		return nil
	})
	return out, err
}

// Baselines implements anomalies.Store.
func (s *AnomalyStore) Baselines(ctx context.Context, org tenant.OrgID, hour int) (map[int64]anomaly.Baseline, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	out := map[int64]anomaly.Baseline{}
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.AnomalyBaselinesOfHour(ctx, int16(hour)) //nolint:gosec // 0 to 167
		if err != nil {
			return wrap(err, "read baselines")
		}
		for _, r := range rows {
			out[r.SeriesID] = anomaly.Baseline{Median: r.Median, MAD: r.Mad, Low: r.Low, High: r.High, N: int(r.Points)}
		}
		return nil
	})
	return out, err
}

// SaveBaselines implements anomalies.Store.
func (s *AnomalyStore) SaveBaselines(ctx context.Context, org tenant.OrgID, hour int, at time.Time, baselines map[int64]anomaly.Baseline) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	p := sqlcdb.UpsertAnomalyBaselinesParams{Hour: int16(hour), ComputedAt: at} //nolint:gosec // 0 to 167
	for id, b := range baselines {
		p.SeriesIds = append(p.SeriesIds, id)
		p.Medians, p.Mads, p.Lows, p.Highs = append(p.Medians, b.Median), append(p.Mads, b.MAD), append(p.Lows, b.Low), append(p.Highs, b.High)
		p.Counts = append(p.Counts, int32(min(b.N, 1<<30))) //nolint:gosec // bounded
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.DeleteOtherAnomalyBaselines(ctx, sqlcdb.DeleteOtherAnomalyBaselinesParams{Hour: p.Hour, SeriesIds: p.SeriesIds}); err != nil {
			return wrap(err, "delete old baselines")
		}
		return wrap(q.UpsertAnomalyBaselines(ctx, p), "save baselines")
	})
}

// BaselineAges implements anomalies.Store.
func (s *AnomalyStore) BaselineAges(ctx context.Context, org tenant.OrgID) (map[int]time.Time, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	out := map[int]time.Time{}
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.AnomalyBaselineAges(ctx)
		if err != nil {
			return wrap(err, "read baseline ages")
		}
		for _, r := range rows {
			out[int(r.Hour)] = r.ComputedAt
		}
		return nil
	})
	return out, err
}

// SaveNotable implements anomalies.Store.
func (s *AnomalyStore) SaveNotable(ctx context.Context, org tenant.OrgID, notable []anomalies.Notable) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	var p sqlcdb.InsertAnomalyPointsParams
	for _, n := range notable {
		p.Times, p.SeriesIds = append(p.Times, n.T), append(p.SeriesIds, n.Series)
		p.Scores, p.Anomalous = append(p.Scores, float32(min(n.Score, 1e30))), append(p.Anomalous, n.Anomalous)
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		return wrap(q.InsertAnomalyPoints(ctx, p), "save notable minutes")
	})
}

// Learning implements anomalies.Reader.
func (s *AnomalyStore) Learning(ctx context.Context, scope tenant.Scope, node identity.NodeID) (time.Time, bool, error) {
	var since time.Time
	found := false
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		t, err := q.NodeLearning(ctx, sqlcdb.NodeLearningParams{NodeID: uuid.UUID(node), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "read node learning")
		}
		since, found = t, true
		return nil
	})
	return since, found, err
}

// ScopedSeries implements anomalies.Reader.
func (s *AnomalyStore) ScopedSeries(ctx context.Context, scope tenant.Scope, metrics []string, nodes []identity.NodeID, limit int) ([]anomalies.SeriesRef, error) {
	ids := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		ids[i] = uuid.UUID(n)
	}
	var out []anomalies.SeriesRef
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ScopedAnomalySeries(ctx, sqlcdb.ScopedAnomalySeriesParams{
			Metrics: metrics, NodeIds: ids, AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope), MaxSeries: int32(min(limit, 1<<30)), //nolint:gosec // bounded
		})
		if err != nil {
			return wrap(err, "list anomaly series")
		}
		out = make([]anomalies.SeriesRef, 0, len(rows))
		for _, r := range rows {
			labels, err := labelsOf(r.Labels)
			if err != nil {
				return err
			}
			out = append(out, anomalies.SeriesRef{ID: r.ID, Node: identity.NodeID(r.NodeID), Metric: r.Metric, Labels: labels})
		}
		return nil
	})
	return out, err
}

// Minutes implements anomalies.Reader.
func (s *AnomalyStore) Minutes(ctx context.Context, scope tenant.Scope, ids []int64, from, to time.Time, limit int) ([]anomalies.Notable, error) {
	var out []anomalies.Notable
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.AnomalyPointsRange(ctx, sqlcdb.AnomalyPointsRangeParams{SeriesIds: ids, FromTs: from, ToTs: to, MaxPoints: int32(min(limit, 1<<30))}) //nolint:gosec // bounded
		if err != nil {
			return wrap(err, "read anomalies")
		}
		out = make([]anomalies.Notable, len(rows))
		for i, r := range rows {
			out[i] = anomalies.Notable{Series: r.SeriesID, T: r.Ts.UTC(), Score: float64(r.Score), Anomalous: r.Anomalous}
		}
		return nil
	})
	return out, err
}

// Top implements anomalies.Reader.
func (s *AnomalyStore) Top(ctx context.Context, scope tenant.Scope, ids []int64, from, to time.Time, limit int) ([]anomalies.Top, error) {
	var out []anomalies.Top
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.AnomalyTop(ctx, sqlcdb.AnomalyTopParams{SeriesIds: ids, FromTs: from, ToTs: to, MaxSeries: int32(min(limit, 1<<30))}) //nolint:gosec // bounded
		if err != nil {
			return wrap(err, "read unusual series")
		}
		out = make([]anomalies.Top, len(rows))
		for i, r := range rows {
			out[i] = anomalies.Top{Series: r.SeriesID, MaxScore: float64(r.MaxScore), Peak: r.Peak.UTC(), Anomalous: int(r.AnomalousMinutes)}
		}
		return nil
	})
	return out, err
}
