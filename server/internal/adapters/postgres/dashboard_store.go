package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// DashboardStore keeps the custom dashboards (M5-23, migration 00060).
type DashboardStore struct{ db *DB }

var _ dashboards.Store = (*DashboardStore)(nil)

// NewDashboardStore returns the store.
func NewDashboardStore(db *DB) *DashboardStore { return &DashboardStore{db: db} }

type dashboardRow struct {
	ID        uuid.UUID
	OwnerID   uuid.UUID
	OwnerName string
	Shared    bool
	Revision  int64
	Spec      []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

func dashboardOf(r dashboardRow) (dashboards.Dashboard, error) {
	var spec dashboard.Spec
	if err := json.Unmarshal(r.Spec, &spec); err != nil {
		return dashboards.Dashboard{}, fmt.Errorf("decode dashboard %s: %w", r.ID, err)
	}
	return dashboards.Dashboard{
		ID: dashboards.ID(r.ID), Owner: identity.UserID(r.OwnerID), OwnerName: r.OwnerName, Shared: r.Shared,
		Revision: r.Revision, Spec: spec, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// List implements dashboards.Store.
func (s *DashboardStore) List(ctx context.Context, scope tenant.Scope, user identity.UserID) ([]dashboards.Dashboard, error) {
	var out []dashboards.Dashboard
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListDashboards(ctx, uuid.UUID(user))
		if err != nil {
			return fmt.Errorf("list dashboards: %w", err)
		}
		for _, r := range rows {
			d, err := dashboardOf(dashboardRow(r))
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// Get implements dashboards.Store.
func (s *DashboardStore) Get(ctx context.Context, scope tenant.Scope, user identity.UserID, id dashboards.ID) (dashboards.Dashboard, error) {
	var out dashboards.Dashboard
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		r, err := q.GetDashboard(ctx, sqlcdb.GetDashboardParams{ID: uuid.UUID(id), UserID: uuid.UUID(user)})
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get dashboard: %w", err)
		}
		out, err = dashboardOf(dashboardRow(r))
		return err
	})
	return out, err
}

// Create implements dashboards.Store.
func (s *DashboardStore) Create(ctx context.Context, scope tenant.Scope, d dashboards.Dashboard, audit access.AuditEntry) error {
	spec, err := json.Marshal(d.Spec)
	if err != nil {
		return fmt.Errorf("encode dashboard: %w", err)
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockDashboardCount(ctx); err != nil {
			return fmt.Errorf("lock dashboards: %w", err)
		}
		n, err := q.CountDashboards(ctx)
		if err != nil {
			return fmt.Errorf("count dashboards: %w", err)
		}
		if n >= dashboards.MaxPerOrganization {
			return dashboards.ErrTooMany
		}
		err = q.CreateDashboard(ctx, sqlcdb.CreateDashboardParams{
			ID: uuid.UUID(d.ID), OwnerID: uuid.UUID(d.Owner), Title: d.Spec.Title, Spec: spec, CreatedAt: d.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("create dashboard: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

func lockDashboard(ctx context.Context, q *sqlcdb.Queries, user identity.UserID, id dashboards.ID) (dashboards.Dashboard, error) {
	r, err := q.LockDashboard(ctx, sqlcdb.LockDashboardParams{ID: uuid.UUID(id), UserID: uuid.UUID(user)})
	if errors.Is(err, pgx.ErrNoRows) {
		return dashboards.Dashboard{}, access.ErrNotFound
	}
	if err != nil {
		return dashboards.Dashboard{}, fmt.Errorf("lock dashboard: %w", err)
	}
	return dashboardOf(dashboardRow(r))
}

// Change implements dashboards.Store.
func (s *DashboardStore) Change(ctx context.Context, scope tenant.Scope, user identity.UserID, id dashboards.ID,
	change func(dashboards.Dashboard) (dashboards.Dashboard, access.AuditEntry, error),
) (dashboards.Dashboard, error) {
	var out dashboards.Dashboard
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		d, err := lockDashboard(ctx, q, user, id)
		if err != nil {
			return err
		}
		next, audit, err := change(d)
		if err != nil {
			return err
		}
		spec, err := json.Marshal(next.Spec)
		if err != nil {
			return fmt.Errorf("encode dashboard: %w", err)
		}
		err = q.UpdateDashboard(ctx, sqlcdb.UpdateDashboardParams{
			ID: uuid.UUID(id), Title: next.Spec.Title, Shared: next.Shared, Revision: next.Revision, Spec: spec, UpdatedAt: next.UpdatedAt,
		})
		if err != nil {
			return fmt.Errorf("update dashboard: %w", err)
		}
		out = next
		return appendAudit(ctx, q, audit)
	})
	return out, err
}

// Delete implements dashboards.Store.
func (s *DashboardStore) Delete(ctx context.Context, scope tenant.Scope, user identity.UserID, id dashboards.ID,
	check func(dashboards.Dashboard) (access.AuditEntry, error),
) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		d, err := lockDashboard(ctx, q, user, id)
		if err != nil {
			return err
		}
		audit, err := check(d)
		if err != nil {
			return err
		}
		if err := q.DeleteDashboard(ctx, uuid.UUID(id)); err != nil {
			return fmt.Errorf("delete dashboard: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// Visible implements dashboards.Store.
func (s *DashboardStore) Visible(ctx context.Context, scope tenant.Scope, nodes []identity.NodeID, groups []tenant.GroupID) (dashboards.Found, error) {
	found := dashboards.Found{Nodes: map[identity.NodeID]bool{}, Groups: map[tenant.GroupID]bool{}}
	nodeIDs := make([]uuid.UUID, 0, len(nodes))
	for _, n := range nodes {
		nodeIDs = append(nodeIDs, uuid.UUID(n))
	}
	ids := make([]uuid.UUID, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, uuid.UUID(g))
	}
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		visibleNodes, err := q.VisibleNodeIDs(ctx, sqlcdb.VisibleNodeIDsParams{Ids: nodeIDs, AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if err != nil {
			return fmt.Errorf("visible nodes: %w", err)
		}
		for _, n := range visibleNodes {
			found.Nodes[identity.NodeID(n)] = true
		}
		visibleGroups, err := q.VisibleGroupIDs(ctx, sqlcdb.VisibleGroupIDsParams{Ids: ids, AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if err != nil {
			return fmt.Errorf("visible groups: %w", err)
		}
		for _, g := range visibleGroups {
			found.Groups[tenant.GroupID(g)] = true
		}
		return nil
	})
	return found, err
}
