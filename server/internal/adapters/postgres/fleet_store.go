package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// FleetStore reads node records for the UI (M1-26).
type FleetStore struct{ db *DB }

var (
	_ fleet.Directory = (*FleetStore)(nil)
	_ fleet.TagStore  = (*FleetStore)(nil)
)

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
			tags, err := decodeTags(r.Tags)
			if err != nil {
				return err
			}
			nodes[i] = fleet.Node{
				ID: identity.NodeID(r.ID), Group: tenant.GroupID(r.GroupID), GroupName: r.GroupName, Hostname: r.Hostname,
				OSID: r.OsID, OSVersion: r.OsVersion, KernelRelease: r.KernelRelease, Architecture: r.Architecture,
				CPUCount: uint32(max(r.CpuCount, 0)), MemoryBytes: uint64(max(r.MemoryBytes, 0)),
				AgentVersion: r.AgentVersion, EnrolledAt: r.CreatedAt, LastSeenAt: r.LastSeenAt,
				LockedAt: r.LockedAt, LockReason: r.LockReason, LastIP: r.LastIp, TakeoverUntil: r.TakeoverUntil,
				Tags: tags, SecuritySource: secevent.Source(r.SecuritySource), LogsVetoed: r.LogsVetoed,
				LearningSince: r.LearningSince,
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fleet nodes: %w", err)
	}
	return nodes, nil
}

// NodeGroup returns the group of a node the scope sees (liveview.Directory); false otherwise.
func (s *FleetStore) NodeGroup(ctx context.Context, scope tenant.Scope, node identity.NodeID) (tenant.GroupID, bool, error) {
	var group uuid.UUID
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		group, err = q.VisibleNodeGroup(ctx, sqlcdb.VisibleNodeGroupParams{
			ID: uuid.UUID(node), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		})
		return err //nolint:wrapcheck // wrapped below
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tenant.GroupID{}, false, nil
	}
	if err != nil {
		return tenant.GroupID{}, false, fmt.Errorf("visible node: %w", err)
	}
	return tenant.GroupID(group), true, nil
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

// SetTags implements fleet.TagStore: the node is locked within the scope's groups, its tags
// replaced and the audit entry written in the same transaction.
func (s *FleetStore) SetTags(ctx context.Context, scope tenant.Scope, node identity.NodeID, tags tag.Tags,
	audit func(tenant.GroupID, tag.Tags) access.AuditEntry,
) (bool, error) {
	encoded, err := json.Marshal(tags)
	if err != nil {
		return false, fmt.Errorf("encode tags: %w", err)
	}
	found := false
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.NodeTagsForUpdate(ctx, sqlcdb.NodeTagsForUpdateParams{ID: uuid.UUID(node), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("find node: %w", err)
		}
		previous, err := decodeTags(row.Tags)
		if err != nil {
			return err
		}
		if err := q.SetNodeTags(ctx, sqlcdb.SetNodeTagsParams{ID: uuid.UUID(node), Tags: encoded}); err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
		found = true
		return appendAudit(ctx, q, audit(tenant.GroupID(row.GroupID), previous))
	})
	if err != nil {
		return false, fmt.Errorf("node tags: %w", err)
	}
	return found, nil
}

// decodeTags reads the jsonb column; nil for none.
func decodeTags(raw []byte) (tag.Tags, error) {
	var tags tag.Tags
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil, fmt.Errorf("decode tags: %w", err)
	}
	if len(tags) == 0 {
		return nil, nil
	}
	return tags, nil
}
