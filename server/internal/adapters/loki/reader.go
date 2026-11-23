package loki

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Reader reads logs for the log explorer (M3-08).
type Reader struct{ client *Client }

var _ logs.Store = (*Reader)(nil)

// NewReader returns the reader.
func NewReader(client *Client) *Reader { return &Reader{client: client} }

// Lines runs a log query.
func (r *Reader) Lines(ctx context.Context, org tenant.OrgID, q logql.Query, from, to time.Time, limit int, forward bool) ([]logs.StoredLine, error) {
	direction := Backward
	if forward {
		direction = Forward
	}
	lines, err := r.client.Query(ctx, org, QueryRange{Query: q, Start: from, End: to, Limit: limit, Direction: direction})
	if err != nil {
		return nil, readError(err)
	}
	out := make([]logs.StoredLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, logs.StoredLine{Labels: l.Labels, At: l.At, Line: l.Line, Metadata: l.Metadata})
	}
	return out, nil
}

// Counts runs a metric query.
func (r *Reader) Counts(ctx context.Context, org tenant.OrgID, q logql.Query, from, to time.Time, step time.Duration) ([]logs.CountSeries, error) {
	series, err := r.client.Counts(ctx, org, q, from, to, step)
	if err != nil {
		return nil, readError(err)
	}
	out := make([]logs.CountSeries, 0, len(series))
	for _, s := range series {
		points := make([]logs.CountPoint, 0, len(s.Points))
		for _, p := range s.Points {
			points = append(points, logs.CountPoint{At: p.At, Value: p.Value})
		}
		out = append(out, logs.CountSeries{Labels: s.Labels, Points: points})
	}
	return out, nil
}

// readError: unreachable or overloaded is logs.ErrUnavailable; anything else stays an error.
func readError(err error) error {
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRateLimited) {
		return fmt.Errorf("%w: %w", logs.ErrUnavailable, err)
	}
	return err
}
