package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/app/kiosk"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// KioskStore keeps the kiosk tokens (M5-26, migration 00061).
type KioskStore struct{ db *DB }

var _ kiosk.Store = (*KioskStore)(nil)

// NewKioskStore returns the store.
func NewKioskStore(db *DB) *KioskStore { return &KioskStore{db: db} }

// List implements kiosk.Store.
func (s *KioskStore) List(ctx context.Context, scope tenant.Scope, dashboard dashboards.ID) ([]kiosk.Token, error) {
	var out []kiosk.Token
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListKioskTokens(ctx, uuid.UUID(dashboard))
		if err != nil {
			return fmt.Errorf("list kiosk tokens: %w", err)
		}
		for _, r := range rows {
			out = append(out, kiosk.Token{
				ID: kiosk.TokenID(r.ID), Dashboard: dashboards.ID(r.DashboardID), Name: r.Name,
				CreatedBy: identity.UserID(r.CreatedBy), CreatedByName: r.CreatedByName, CreatedAt: r.CreatedAt,
				ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt,
			})
		}
		return nil
	})
	return out, err
}

// Create implements kiosk.Store: the dashboard is locked, so that unsharing it at the same time
// cannot leave a token behind.
func (s *KioskStore) Create(ctx context.Context, scope tenant.Scope, t kiosk.Token, hash [sha256.Size]byte, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		shared, err := q.LockSharedDashboard(ctx, uuid.UUID(t.Dashboard))
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock dashboard: %w", err)
		}
		if !shared {
			return kiosk.ErrNotShared
		}
		open, err := q.CountOpenKioskTokens(ctx, sqlcdb.CountOpenKioskTokensParams{DashboardID: uuid.UUID(t.Dashboard), At: t.CreatedAt})
		if err != nil {
			return fmt.Errorf("count kiosk tokens: %w", err)
		}
		if open >= kiosk.MaxOpen {
			return kiosk.ErrTooMany
		}
		err = q.CreateKioskToken(ctx, sqlcdb.CreateKioskTokenParams{
			ID: uuid.UUID(t.ID), DashboardID: uuid.UUID(t.Dashboard), Name: t.Name, TokenHash: hash[:],
			CreatedBy: uuid.UUID(t.CreatedBy), CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("create kiosk token: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// Revoke implements kiosk.Store; the audit entry gets the dashboard and the name.
func (s *KioskStore) Revoke(ctx context.Context, scope tenant.Scope, id kiosk.TokenID, at time.Time, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		r, err := q.RevokeKioskToken(ctx, sqlcdb.RevokeKioskTokenParams{ID: uuid.UUID(id), At: &at})
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke kiosk token: %w", err)
		}
		audit.Details = withDetails(audit.Details, "dashboard", r.DashboardID.String(), "name", r.Name)
		return appendAudit(ctx, q, audit)
	})
}

// Lookup implements kiosk.Store through the SECURITY DEFINER function: only usable tokens.
func (s *KioskStore) Lookup(ctx context.Context, hash [sha256.Size]byte, at time.Time) (kiosk.Found, bool, error) {
	rows, err := s.db.withoutTenant().KioskTokenByHash(ctx, sqlcdb.KioskTokenByHashParams{Hash: hash[:], At: at})
	if err != nil {
		return kiosk.Found{}, false, fmt.Errorf("kiosk token by hash: %w", err)
	}
	if len(rows) != 1 || len(rows[0].TokenHash) != sha256.Size {
		return kiosk.Found{}, false, nil
	}
	r := rows[0]
	f := kiosk.Found{
		Token: kiosk.TokenID(r.ID), Org: tenant.OrgID(r.OrgID), Dashboard: dashboards.ID(r.DashboardID), Creator: identity.UserID(r.CreatedBy),
	}
	copy(f.Hash[:], r.TokenHash)
	return f, true, nil
}

// Touch implements kiosk.Store.
func (s *KioskStore) Touch(ctx context.Context, scope tenant.Scope, id kiosk.TokenID, at time.Time) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.TouchKioskToken(ctx, sqlcdb.TouchKioskTokenParams{ID: uuid.UUID(id), At: &at}); err != nil {
			return fmt.Errorf("touch kiosk token: %w", err)
		}
		return nil
	})
}
