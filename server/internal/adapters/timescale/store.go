// Package timescale is the metrics.Store on TimescaleDB (ADR 0001, M1-22).
//
// Writing: series are registered in the organization's tenant transaction (RLS on series, the
// node must belong to the organization) and cached; the 10 s windows go in with COPY, and the
// node's persisted batch sequence number advances in the same transaction.
//
// Reading: series are looked up with RLS and the scope's node groups; points come from SECURITY
// DEFINER functions, because sermon_app has no SELECT on the metric tables (spike M0-09).
package timescale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ErrUnknownNode means a node is not part of the organization.
var ErrUnknownNode = errors.New("node not found in the organization")

// Store implements metrics.Store.
type Store struct {
	db *postgres.DB

	mu     sync.RWMutex
	series map[seriesKey]int32
}

var _ metrics.Store = (*Store)(nil)

type seriesKey struct {
	org    tenant.OrgID
	node   identity.NodeID
	metric string
	labels string
}

// New returns the store on db.
func New(db *postgres.DB) *Store { return &Store{db: db, series: map[seriesKey]int32{}} }

func keyOf(org tenant.OrgID, a metrics.Aggregate) seriesKey {
	return seriesKey{org: org, node: a.Node, metric: a.Metric, labels: a.Labels.Key()}
}

// Write implements metrics.Store.
func (s *Store) Write(ctx context.Context, org tenant.OrgID, aggregates []metrics.Aggregate, persisted map[identity.NodeID]uint64) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("write metrics: %w", err)
	}
	var registered map[seriesKey]int32
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		ids, fresh, err := s.seriesIDs(ctx, q, org, aggregates)
		if err != nil {
			return err
		}
		registered = fresh
		rows := make([]sqlcdb.CopyMetricWindowsParams, len(aggregates))
		for i, a := range aggregates {
			rows[i] = sqlcdb.CopyMetricWindowsParams{Ts: a.Start, SeriesID: ids[keyOf(org, a)], Avg: a.Avg, Min: a.Min, Max: a.Max}
		}
		if len(rows) > 0 {
			if _, err := q.CopyMetricWindows(ctx, rows); err != nil {
				return fmt.Errorf("copy metric windows: %w", err)
			}
		}
		for node, seq := range persisted {
			n, err := q.AdvanceIngestSeq(ctx, sqlcdb.AdvanceIngestSeqParams{Seq: int64(min(seq, 1<<63-1)), NodeID: uuid.UUID(node)})
			if err != nil {
				return fmt.Errorf("advance ingest seq: %w", err)
			}
			if n == 0 {
				return ErrUnknownNode
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("write metrics: %w", err)
	}
	// Cache only after the commit: IDs of a rolled back transaction don't exist.
	s.mu.Lock()
	for key, id := range registered {
		s.series[key] = id
	}
	s.mu.Unlock()
	return nil
}

// seriesIDs returns the IDs of all series of the aggregates, registering missing ones; fresh are
// the IDs not yet in the cache.
func (s *Store) seriesIDs(ctx context.Context, q *sqlcdb.Queries, org tenant.OrgID, aggregates []metrics.Aggregate) (map[seriesKey]int32, map[seriesKey]int32, error) {
	ids := map[seriesKey]int32{}
	missing := map[seriesKey]metrics.Aggregate{}
	s.mu.RLock()
	for _, a := range aggregates {
		key := keyOf(org, a)
		if id, ok := s.series[key]; ok {
			ids[key] = id
		} else {
			missing[key] = a
		}
	}
	s.mu.RUnlock()
	fresh := map[seriesKey]int32{}
	// A series inserted concurrently by another transaction is not visible to the first attempt.
	for attempt := 0; attempt < 3 && len(missing) > 0; attempt++ {
		params := sqlcdb.EnsureSeriesParams{}
		for _, a := range missing {
			labels, err := json.Marshal(nonNil(a.Labels))
			if err != nil {
				return nil, nil, fmt.Errorf("encode labels: %w", err)
			}
			params.NodeIds = append(params.NodeIds, uuid.UUID(a.Node))
			params.Metrics = append(params.Metrics, a.Metric)
			params.Labels = append(params.Labels, labels)
		}
		rows, err := q.EnsureSeries(ctx, params)
		if err != nil {
			if isForeignKeyViolation(err) {
				return nil, nil, ErrUnknownNode
			}
			return nil, nil, fmt.Errorf("register series: %w", err)
		}
		for _, row := range rows {
			var labels metrics.Labels
			if err := json.Unmarshal(row.Labels, &labels); err != nil {
				return nil, nil, fmt.Errorf("decode labels: %w", err)
			}
			key := seriesKey{org: org, node: identity.NodeID(row.NodeID), metric: row.Metric, labels: labels.Key()}
			ids[key] = row.ID
			fresh[key] = row.ID
			delete(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, nil, errors.New("register series: concurrent registration did not settle")
	}
	return ids, fresh, nil
}

// PersistedSeq implements metrics.Store.
func (s *Store) PersistedSeq(ctx context.Context, org tenant.OrgID, node identity.NodeID) (uint64, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return 0, fmt.Errorf("persisted seq: %w", err)
	}
	var seq int64
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		value, err := q.GetIngestSeq(ctx, uuid.UUID(node))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnknownNode
		}
		if err != nil {
			return fmt.Errorf("get ingest seq: %w", err)
		}
		seq = value
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("persisted seq: %w", err)
	}
	return uint64(max(seq, 0)), nil
}

// Query implements metrics.Store.
func (s *Store) Query(ctx context.Context, scope tenant.Scope, q metrics.Query) ([]metrics.Series, error) {
	filter, err := json.Marshal(nonNil(q.Labels))
	if err != nil {
		return nil, fmt.Errorf("encode label filter: %w", err)
	}
	nodes := make([]uuid.UUID, len(q.Nodes))
	for i, node := range q.Nodes {
		nodes[i] = uuid.UUID(node)
	}
	groups := make([]uuid.UUID, 0)
	for _, group := range scope.Groups() {
		groups = append(groups, uuid.UUID(group))
	}
	var out []metrics.Series
	err = s.db.InTenant(ctx, scope, func(db *sqlcdb.Queries) error {
		found, err := db.FindSeries(ctx, sqlcdb.FindSeriesParams{
			Metric: q.Metric, NodeIds: nodes, LabelFilter: filter,
			AllGroups: scope.AllGroups(), GroupIds: groups, MaxSeries: metrics.MaxSeries,
		})
		if err != nil {
			return fmt.Errorf("find series: %w", err)
		}
		if len(found) == 0 {
			return nil
		}
		index := map[int32]int{}
		ids := make([]int32, len(found))
		out = make([]metrics.Series, len(found))
		for i, row := range found {
			var labels metrics.Labels
			if err := json.Unmarshal(row.Labels, &labels); err != nil {
				return fmt.Errorf("decode labels: %w", err)
			}
			ids[i] = row.ID
			index[row.ID] = i
			out[i] = metrics.Series{Node: identity.NodeID(row.NodeID), Labels: labels}
		}
		points, err := s.points(ctx, db, q, ids)
		if err != nil {
			return err
		}
		for _, p := range points {
			series := &out[index[p.series]]
			series.Points = append(series.Points, p.point)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	// Series without points in the range are left out.
	result := out[:0]
	for _, series := range out {
		if len(series.Points) > 0 {
			result = append(result, series)
		}
	}
	return result, nil
}

type seriesPoint struct {
	series int32
	point  metrics.Point
}

func (s *Store) points(ctx context.Context, db *sqlcdb.Queries, q metrics.Query, ids []int32) ([]seriesPoint, error) {
	step := pgtype.Interval{Microseconds: max(q.Step, q.Source.Resolution()).Microseconds(), Valid: true}
	var out []seriesPoint
	switch q.Source {
	case metrics.SourceRaw:
		rows, err := db.PointsRaw(ctx, sqlcdb.PointsRawParams{SeriesIds: ids, FromTs: q.From, ToTs: q.To, Step: step})
		if err != nil {
			return nil, fmt.Errorf("read points: %w", err)
		}
		for _, r := range rows {
			out = append(out, seriesPoint{r.SeriesID, metrics.Point{Time: r.Bucket.UTC(), Avg: r.Avg, Min: r.Min, Max: r.Max}})
		}
	case metrics.Source1m:
		rows, err := db.Points1m(ctx, sqlcdb.Points1mParams{SeriesIds: ids, FromTs: q.From, ToTs: q.To, Step: step})
		if err != nil {
			return nil, fmt.Errorf("read points: %w", err)
		}
		for _, r := range rows {
			out = append(out, seriesPoint{r.SeriesID, metrics.Point{Time: r.Bucket.UTC(), Avg: r.Avg, Min: r.Min, Max: r.Max}})
		}
	case metrics.Source1h:
		rows, err := db.Points1h(ctx, sqlcdb.Points1hParams{SeriesIds: ids, FromTs: q.From, ToTs: q.To, Step: step})
		if err != nil {
			return nil, fmt.Errorf("read points: %w", err)
		}
		for _, r := range rows {
			out = append(out, seriesPoint{r.SeriesID, metrics.Point{Time: r.Bucket.UTC(), Avg: r.Avg, Min: r.Min, Max: r.Max}})
		}
	default:
		return nil, metrics.ErrInvalidQuery
	}
	return out, nil
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func nonNil(labels metrics.Labels) metrics.Labels {
	if labels == nil {
		return metrics.Labels{}
	}
	return labels
}
