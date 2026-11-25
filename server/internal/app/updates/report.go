// Package updates keeps what the nodes could update (KONZEPT §14.1): the reports the ingest
// stores (M4-09), read for the callers with ViewNodes, and the update policies of the groups
// (M4-10).
package updates

import (
	"context"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// DefaultOrgPackageLimit bounds the packages stored for all nodes of an organization; beyond it
// a report keeps its counts but stores only the packages that still fit.
const DefaultOrgPackageLimit = 1_000_000

// NodeUpdates is a node with its current report.
type NodeUpdates struct {
	Node     identity.NodeID
	Hostname string
	Group    tenant.GroupID
	// Report is nil before the first one.
	Report     *update.Report
	ReceivedAt time.Time
	// PackagesOmitted: fewer packages are stored than the report counts (the agent's or the
	// organization's limit).
	PackagesOmitted bool
	// OldestSecurity is when the oldest open security update was first listed.
	OldestSecurity *time.Time
}

// Status is the node's status.
func (n NodeUpdates) Status() update.Status {
	if n.Report == nil {
		return update.StatusOf(update.AvailabilityUnknown, 0, 0, false)
	}
	return update.StatusOf(n.Report.Availability, n.Report.Total, n.Report.Security, true)
}

// Store reads the reports within a scope (only nodes of the scope's groups).
type Store interface {
	// Node returns the node with its report and packages; false if it isn't in the scope.
	Node(ctx context.Context, scope tenant.Scope, node identity.NodeID) (NodeUpdates, bool, error)
	// Fleet returns the nodes of the scope with their reports without packages, by host name.
	Fleet(ctx context.Context, scope tenant.Scope) ([]NodeUpdates, error)
}
