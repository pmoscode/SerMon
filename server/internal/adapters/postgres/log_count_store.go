package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// LogCountStore keeps the counts of the lines Loki stored (V1-24, ADR 0044): the ingest adds
// them, the log histogram reads them, the retention deletes them. Only through the SECURITY
// DEFINER functions of migration 00084, in the tenant of the organization.
type LogCountStore struct{ db *DB }

var (
	_ ingest.LogCounter = (*LogCountStore)(nil)
	_ logs.CountStore   = (*LogCountStore)(nil)
)

// NewLogCountStore returns the store.
func NewLogCountStore(db *DB) *LogCountStore { return &LogCountStore{db: db} }

// severityCodes are the codes of the severities in the store: as ingest.LogEntry, 0 unknown.
var severityCodes = map[logql.Severity]int16{
	logql.SeverityUnknown: 0, logql.SeverityEmergency: 1, logql.SeverityAlert: 2, logql.SeverityCritical: 3,
	logql.SeverityError: 4, logql.SeverityWarning: 5, logql.SeverityNotice: 6, logql.SeverityInfo: 7, logql.SeverityDebug: 8,
}

var sourceCodes = map[logql.Source]int16{
	logql.SourceJournal: int16(ingest.LogSourceJournal), logql.SourceFile: int16(ingest.LogSourceFile),
	logql.SourceContainer: int16(ingest.LogSourceContainer),
}

// CountLogs implements ingest.LogCounter.
func (s *LogCountStore) CountLogs(ctx context.Context, org tenant.OrgID, node identity.NodeID, counts []ingest.LogCount) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	p := sqlcdb.AddLogCountsParams{
		NodeID: uuid.UUID(node), Seqs: make([]int64, 0, len(counts)), Ats: make([]time.Time, 0, len(counts)),
		Sources: make([]int16, 0, len(counts)), Severities: make([]int16, 0, len(counts)), Lines: make([]int64, 0, len(counts)),
	}
	for _, c := range counts {
		if c.Seq > math.MaxInt64 || c.Lines > math.MaxInt64 || c.Severity > ingest.MaxSeverity {
			return fmt.Errorf("log counts: implausible count %+v", c)
		}
		p.Seqs = append(p.Seqs, int64(c.Seq))
		p.Ats = append(p.Ats, c.Minute)
		p.Sources = append(p.Sources, int16(c.Source))
		p.Severities = append(p.Severities, int16(c.Severity))
		p.Lines = append(p.Lines, int64(c.Lines))
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		_, err := q.AddLogCounts(ctx, p)
		return wrap(err, "add log counts")
	})
}

// Counts implements logs.CountStore.
func (s *LogCountStore) Counts(ctx context.Context, org tenant.OrgID, cq logs.CountQuery) ([]logs.CountSeries, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	if cq.Step < time.Minute || cq.Step%time.Minute != 0 || cq.Step > logql.MaxStep {
		return nil, fmt.Errorf("%w: a step of whole minutes up to %v", logs.ErrInvalid, logql.MaxStep)
	}
	p := sqlcdb.LogCountsRangeParams{
		NodeIds: make([]uuid.UUID, 0, len(cq.Nodes)), After: cq.After, Until: cq.Until,
		StepSeconds: int32(cq.Step / time.Second), Sources: []int16{}, Severities: []int16{},
	}
	for _, n := range cq.Nodes {
		p.NodeIds = append(p.NodeIds, uuid.UUID(n))
	}
	for _, src := range cq.Sources {
		code, ok := sourceCodes[src]
		if !ok {
			return nil, fmt.Errorf("%w: unknown source %q", logs.ErrInvalid, src)
		}
		p.Sources = append(p.Sources, code)
	}
	for _, sev := range cq.Severities {
		code, ok := severityCodes[sev]
		if !ok {
			return nil, fmt.Errorf("%w: unknown severity %q", logs.ErrInvalid, sev)
		}
		p.Severities = append(p.Severities, code)
	}
	var rows []sqlcdb.LogCountsRangeRow
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		rows, err = q.LogCountsRange(ctx, p)
		return wrap(err, "read log counts")
	})
	if err != nil {
		return nil, err
	}
	index := map[int16]int{}
	var out []logs.CountSeries
	for _, r := range rows {
		i, ok := index[r.Severity]
		if !ok {
			i = len(out)
			index[r.Severity] = i
			out = append(out, logs.CountSeries{Labels: map[string]string{"severity": string(severityOf(r.Severity))}})
		}
		out[i].Points = append(out[i].Points, logs.CountPoint{At: r.At.UTC(), Value: float64(r.Lines)})
	}
	return out, nil
}

func severityOf(code int16) logql.Severity {
	for sev, c := range severityCodes {
		if c == code {
			return sev
		}
	}
	return logql.SeverityUnknown
}

// ExpireLogCounts deletes the organization's counts up to before (its retention, M5-30) and
// returns the rows deleted.
func (s *LogCountStore) ExpireLogCounts(ctx context.Context, org tenant.OrgID, before time.Time) (int64, error) {
	scope, err := orgScope(org)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		n, err = q.ExpireLogCounts(ctx, before)
		return wrap(err, "expire log counts")
	})
	return n, err
}
