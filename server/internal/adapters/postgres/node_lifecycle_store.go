package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// NodeLifecycleStore persists deactivations and deletions of nodes (M5-29, ADR 0041).
type NodeLifecycleStore struct{ db *DB }

var _ nodelife.Store = (*NodeLifecycleStore)(nil)

// NewNodeLifecycleStore returns the store.
func NewNodeLifecycleStore(db *DB) *NodeLifecycleStore { return &NodeLifecycleStore{db: db} }

// State returns the node within the scope's groups.
func (s *NodeLifecycleStore) State(ctx context.Context, scope tenant.Scope, node identity.NodeID) (nodelife.State, error) {
	var st nodelife.State
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.NodeLifecycleState(ctx, sqlcdb.NodeLifecycleStateParams{
			ID: uuid.UUID(node), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load node: %w", err)
		}
		st = nodelife.State{Group: tenant.GroupID(row.GroupID), Hostname: row.Hostname, DeactivatedAt: row.DeactivatedAt, DeletionDueAt: row.DeletionDueAt}
		return nil
	})
	return st, err
}

// Deactivate marks the node deactivated and revokes all its certificates.
func (s *NodeLifecycleStore) Deactivate(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time, audit access.AuditEntry) error {
	return s.deactivate(ctx, org, node, at, nil, audit)
}

// ScheduleDeletion deactivates the node and sets its deadline.
func (s *NodeLifecycleStore) ScheduleDeletion(ctx context.Context, org tenant.OrgID, node identity.NodeID, at, due time.Time, audit access.AuditEntry) error {
	return s.deactivate(ctx, org, node, at, &due, audit)
}

func (s *NodeLifecycleStore) deactivate(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time, due *time.Time, audit access.AuditEntry) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		n, err := q.DeactivateNode(ctx, sqlcdb.DeactivateNodeParams{At: &at, Due: due, ID: uuid.UUID(node)})
		if err != nil {
			return fmt.Errorf("deactivate node: %w", err)
		}
		if n != 1 {
			return access.ErrNotFound
		}
		if err := q.RevokeNodeCertificates(ctx, sqlcdb.RevokeNodeCertificatesParams{At: &at, NodeID: uuid.UUID(node)}); err != nil {
			return fmt.Errorf("revoke certificates: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// CancelDeletion lifts the deadline.
func (s *NodeLifecycleStore) CancelDeletion(ctx context.Context, org tenant.OrgID, node identity.NodeID, audit access.AuditEntry) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		n, err := q.CancelNodeDeletion(ctx, uuid.UUID(node))
		if err != nil {
			return fmt.Errorf("cancel deletion: %w", err)
		}
		if n != 1 {
			return nodelife.ErrNotScheduled
		}
		return appendAudit(ctx, q, audit)
	})
}

// DueForDeletion returns deactivated nodes of all organizations that are due.
func (s *NodeLifecycleStore) DueForDeletion(ctx context.Context, at time.Time, limit int) ([]nodelife.Due, error) {
	rows, err := s.db.withoutTenant().NodesDueForDeletion(ctx, sqlcdb.NodesDueForDeletionParams{At: at, MaxNodes: int32(min(limit, nodelife.PurgeBatch))}) //nolint:gosec // G115: at most PurgeBatch
	if err != nil {
		return nil, fmt.Errorf("due nodes: %w", err)
	}
	out := make([]nodelife.Due, len(rows))
	for i, r := range rows {
		out[i] = nodelife.Due{Org: tenant.OrgID(r.OrgID), Node: identity.NodeID(r.NodeID)}
	}
	return out, nil
}

// Purge deletes the node with its data (node_purge) and writes node.purge into the audit log of
// its organization, in one transaction.
func (s *NodeLifecycleStore) Purge(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		hostname, err := q.PurgeNode(ctx, sqlcdb.PurgeNodeParams{OrgID: uuid.UUID(org), NodeID: uuid.UUID(node), At: at})
		if err != nil {
			return fmt.Errorf("purge node: %w", err)
		}
		return appendAudit(ctx, q, access.AuditEntry{
			Action: "node.purge", Target: "node:" + node.String(), Details: map[string]string{"hostname": hostname},
		})
	})
}

func (s *NodeLifecycleStore) inOrg(ctx context.Context, org tenant.OrgID, fn func(*sqlcdb.Queries) error) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("organization: %w", err)
	}
	return s.db.InTenant(ctx, scope, fn)
}
