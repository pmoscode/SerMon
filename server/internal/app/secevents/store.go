// Package secevents stores and queries the security events of the nodes (KONZEPT §11, M2-07):
// written by the ingest, deduplicated by the batch sequence number, kept for a year in monthly
// partitions, listed for callers with ViewSecurityEvents.
package secevents

import (
	"context"
	"net/netip"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Batch is one batch of an agent with its events.
type Batch struct {
	Seq    uint64
	Events []secevent.Event
}

// Writer stores the events of a node.
type Writer interface {
	// Write stores the batches whose sequence number is above the node's stored one, together
	// with the new number; it returns the batches that were stored (the rest were duplicates).
	Write(ctx context.Context, org tenant.OrgID, node identity.NodeID, batches []Batch) ([]Batch, error)
}

// Cursor is the position after the last event of a page.
type Cursor struct {
	At time.Time
	ID int64
}

// Filter selects events; From and To are required, everything else narrows.
type Filter struct {
	From, To time.Time
	Nodes    []identity.NodeID
	Kinds    []secevent.Kind
	User     *string
	Network  *netip.Prefix
	Before   *Cursor
	Limit    int
}

// Store reads events within a scope (only nodes of the scope's groups).
type Store interface {
	List(ctx context.Context, scope tenant.Scope, f Filter) ([]secevent.Event, error)
}

// Partitions are the monthly partitions (instance level). They are created ahead by migrations;
// the application only drops expired ones.
type Partitions interface {
	// Expire drops the partitions that end at or before before.
	Expire(ctx context.Context, before time.Time) (int, error)
	// Horizon is the end of the last partition: events after it can't be stored.
	Horizon(ctx context.Context) (time.Time, error)
}
