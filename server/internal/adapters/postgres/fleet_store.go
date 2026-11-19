package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// FleetStore reads node records for the UI (M1-26).
type FleetStore struct{ db *DB }

var _ fleet.Directory = (*FleetStore)(nil)

// NewFleetStore returns the store.
func NewFleetStore(db *DB) *FleetStore { return &FleetStore{db: db} }

// List implements fleet.Directory.
func (s *FleetStore) List(ctx context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	var nodes []fleet.Node
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListVisibleNodes(ctx, sqlcdb.ListVisibleNodesParams{AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if err != nil {
			return fmt.Errorf("list nodes: %w", err)
		}
		nodes = make([]fleet.Node, len(rows))
		for i, r := range rows {
			nodes[i] = fleet.Node{
				ID: identity.NodeID(r.ID), Group: tenant.GroupID(r.GroupID), GroupName: r.GroupName, Hostname: r.Hostname,
				OSID: r.OsID, OSVersion: r.OsVersion, KernelRelease: r.KernelRelease, Architecture: r.Architecture,
				CPUCount: uint32(max(r.CpuCount, 0)), MemoryBytes: uint64(max(r.MemoryBytes, 0)),
				AgentVersion: r.AgentVersion, EnrolledAt: r.CreatedAt, LastSeenAt: r.LastSeenAt,
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fleet nodes: %w", err)
	}
	return nodes, nil
}

// HasGroup implements fleet.Directory.
func (s *FleetStore) HasGroup(ctx context.Context, scope tenant.Scope, group tenant.GroupID) (bool, error) {
	var exists bool
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		exists, err = q.VisibleGroupExists(ctx, sqlcdb.VisibleGroupExistsParams{
			GroupID: uuid.UUID(group), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		})
		if err != nil {
			return fmt.Errorf("group exists: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("fleet group: %w", err)
	}
	return exists, nil
}
