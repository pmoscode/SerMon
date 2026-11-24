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
	// NewSourcesOnly keeps the successful logins from a source new to the node (M3-16).
	NewSourcesOnly bool
	Before         *Cursor
	Limit          int
}

// OverviewQuery selects what an overview evaluates (M3-16): the events of the range and nodes,
// failed logins in buckets of Step, the Top addresses and users.
type OverviewQuery struct {
	From, To time.Time
	Nodes    []identity.NodeID
	Step     time.Duration
	Top      int
}

// Point is the count of the bucket starting at At.
type Point struct {
	At    time.Time
	Count int64
}

// NodeSeries are the buckets of one node with events, oldest first.
type NodeSeries struct {
	Node   identity.NodeID
	Points []Point
}

// Count is an address or user name with its failed logins: how many, on how many nodes, the
// latest and how many of them are unverified (ADR 0025).
type Count struct {
	Value      string
	Count      int64
	Nodes      int
	Last       time.Time
	Unverified int64
	// Country is the ISO code of an address (M3-18), empty without GeoIP.
	Country string
}

// Totals count the events of a range.
type Totals struct {
	FailedLogins, Logins, NewSourceLogins, RootLogins, SudoFailures int64
}

// Overview are the evaluations of a range. Failed logins are ssh.login.failed and
// ssh.login.invalid_user; sudo failures sudo.auth.failed and sudo.not_allowed.
type Overview struct {
	Step             time.Duration
	FailedLogins     []NodeSeries
	TopIPs, TopUsers []Count
	Totals           Totals
	// GeoNotice is the licence notice of the countries; empty without GeoIP.
	GeoNotice string
}

// Geo knows the countries of public addresses (M3-18, a local database of the operator).
type Geo interface {
	// Country returns the ISO 3166-1 alpha-2 code, empty if unknown or not public.
	Country(ip netip.Addr) string
	// Notice is the attribution the licence of the data asks for.
	Notice() string
}

// Store reads events within a scope (only nodes of the scope's groups).
type Store interface {
	List(ctx context.Context, scope tenant.Scope, f Filter) ([]secevent.Event, error)
	Overview(ctx context.Context, scope tenant.Scope, q OverviewQuery) (Overview, error)
	// Hostnames returns the host names of the nodes of the scope among ids.
	Hostnames(ctx context.Context, scope tenant.Scope, ids []identity.NodeID) (map[identity.NodeID]string, error)
}

// Partitions are the monthly partitions (instance level). They are created ahead by migrations;
// the application only drops expired ones.
type Partitions interface {
	// Expire drops the partitions that end at or before before.
	Expire(ctx context.Context, before time.Time) (int, error)
	// Horizon is the end of the last partition: events after it can't be stored.
	Horizon(ctx context.Context) (time.Time, error)
}
