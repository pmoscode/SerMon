package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Reader answers metric queries from the ring buffer or the store, whichever holds the range at
// the requested resolution (M1-23).
type Reader struct {
	store Store
	ring  *Ring
	now   func() time.Time
}

// NewReader returns a reader over store and ring; now is the server clock.
func NewReader(store Store, ring *Ring, now func() time.Time) *Reader {
	return &Reader{store: store, ring: ring, now: now}
}

// Query normalizes q (validation, source, step) and reads it within the scope.
func (r *Reader) Query(ctx context.Context, scope tenant.Scope, q Query) ([]Series, error) {
	q, err := q.Normalize(r.now())
	if err != nil {
		return nil, err
	}
	if q.Source == SourceRing {
		return r.ring.Query(scope, q), nil
	}
	series, err := r.store.Query(ctx, scope, q)
	if err != nil {
		return nil, fmt.Errorf("query metric store: %w", err)
	}
	return series, nil
}
