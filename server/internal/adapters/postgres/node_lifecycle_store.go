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

// Change locks the node within the scope's groups, lets decide choose the change and makes it
// with its audit entry, in one transaction: a node moved to another group meanwhile is not found.
func (s *NodeLifecycleStore) Change(ctx context.Context, scope tenant.Scope, node identity.NodeID, decide func(nodelife.State) (nodelife.Change, error)) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.NodeLifecycleState(ctx, sqlcdb.NodeLifecycleStateParams{
			ID: uuid.UUID(node), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load node: %w", err)
		}
		ch, err := decide(nodelife.State{
			Group: tenant.GroupID(row.GroupID), Hostname: row.Hostname, DeactivatedAt: row.DeactivatedAt,
			DeletionDueAt: row.DeletionDueAt, PurgeStartedAt: row.PurgeStartedAt,
		})
		if err != nil {
			return err
		}
		switch ch.Op {
		case nodelife.OpNone:
			return nil
		case nodelife.OpDeactivate:
			err = deactivate(ctx, q, node, ch.At, nil)
		case nodelife.OpScheduleDeletion:
			err = deactivate(ctx, q, node, ch.At, &ch.Due)
		case nodelife.OpCancelDeletion:
			err = cancelDeletion(ctx, q, node)
		default:
			err = fmt.Errorf("unknown node change %d", ch.Op)
		}
		if err != nil {
			return err
		}
		return appendAudit(ctx, q, ch.Audit)
	})
}

func deactivate(ctx context.Context, q *sqlcdb.Queries, node identity.NodeID, at time.Time, due *time.Time) error {
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
	return nil
}

func cancelDeletion(ctx context.Context, q *sqlcdb.Queries, node identity.NodeID) error {
	n, err := q.CancelNodeDeletion(ctx, uuid.UUID(node))
	if err != nil {
		return fmt.Errorf("cancel deletion: %w", err)
	}
	if n != 1 {
		return nodelife.ErrNotScheduled
	}
	return nil
}

// StartPurge marks the node as being deleted if it is deactivated and due.
func (s *NodeLifecycleStore) StartPurge(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time) (bool, error) {
	var marked bool
	err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		n, err := q.StartNodePurge(ctx, sqlcdb.StartNodePurgeParams{At: &at, ID: uuid.UUID(node)})
		if err != nil {
			return fmt.Errorf("mark node: %w", err)
		}
		marked = n == 1
		return nil
	})
	return marked, err
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
