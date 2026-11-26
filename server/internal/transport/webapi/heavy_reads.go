package webapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
)

// HeavyReads bounds the dashboards' reads of a fleet (anomalies, capacity, heatmap) running at
// once: each holds a connection of the database pool every organization shares for up to
// heavyReadTimeout, so together they take at most half of it, a session, a user and an
// organization only part of that (T106, M4-31).
type HeavyReads struct{ limiter *streamLimiter }

// NewHeavyReads returns the limits for a database pool of poolSize connections.
func NewHeavyReads(poolSize int) *HeavyReads {
	total := max(poolSize/2, 2)
	return &HeavyReads{limiter: newStreamLimiter(streamLimits{
		Session: min(2, total), User: min(3, total), Org: max(total/2, min(2, total)), Total: total,
	})}
}

// orNew returns r, or for a handler built without shared limits (tests) those of pgx's
// smallest pool.
func (r *HeavyReads) orNew() *HeavyReads {
	if r == nil {
		return NewHeavyReads(4)
	}
	return r
}

var errTooManyReads = errors.New("too many dashboard reads at once, wait for the running ones")

// admit takes a slot for a read and bounds its time; release frees both.
func (r *HeavyReads) admit(ctx context.Context, c access.Caller) (context.Context, func(), error) {
	s, _ := sessionFrom(ctx)
	k := streamKeyOf(s, c)
	if !r.limiter.acquire(k) {
		return nil, nil, connect.NewError(connect.CodeResourceExhausted, errTooManyReads)
	}
	readCtx, cancel := context.WithTimeout(ctx, heavyReadTimeout)
	return readCtx, func() {
		cancel()
		r.limiter.release(k)
	}, nil
}
