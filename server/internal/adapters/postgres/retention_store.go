package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/orgretention"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// RetentionStore persists the retention of organizations (M5-30).
type RetentionStore struct{ db *DB }

var _ orgretention.Store = (*RetentionStore)(nil)

// NewRetentionStore returns the store.
func NewRetentionStore(db *DB) *RetentionStore { return &RetentionStore{db: db} }

// Get returns the settings and the cap of the scope's organization.
func (s *RetentionStore) Get(ctx context.Context, scope tenant.Scope) (retention.Settings, uint32, error) {
	var set retention.Settings
	var capDays uint32
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.GetRetention(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load retention: %w", err)
		}
		set = retention.Settings{LogDays: retentionDays(row.LogRetentionDays), EventDays: retentionDays(row.EventRetentionDays)}
		capDays = uint32(max(row.RetentionCapDays.Int32, 0))
		return nil
	})
	return set, capDays, err
}

func retentionDays(v pgtype.Int2) uint32 {
	if !v.Valid {
		return 0
	}
	return uint32(max(v.Int16, 0))
}

func retentionDaysArg(v uint32) pgtype.Int2 {
	if v == 0 {
		return pgtype.Int2{}
	}
	return pgtype.Int2{Int16: int16(min(v, retention.MaxEventDays)), Valid: true}
}

// Set stores the settings with the audit entry.
func (s *RetentionStore) Set(ctx context.Context, scope tenant.Scope, set retention.Settings, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.SetRetention(ctx, sqlcdb.SetRetentionParams{LogDays: retentionDaysArg(set.LogDays), EventDays: retentionDaysArg(set.EventDays)})
		if err != nil {
			return fmt.Errorf("set retention: %w", err)
		}
		if n != 1 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// Configured returns the organizations with a retention of their own.
func (s *RetentionStore) Configured(ctx context.Context) ([]orgretention.OrgSettings, error) {
	rows, err := s.db.withoutTenant().OrganizationRetentions(ctx)
	if err != nil {
		return nil, fmt.Errorf("organization retentions: %w", err)
	}
	out := make([]orgretention.OrgSettings, len(rows))
	for i, r := range rows {
		out[i] = orgretention.OrgSettings{
			Org:      tenant.OrgID(r.OrgID),
			Settings: retention.Settings{LogDays: uint32(max(r.LogDays, 0)), EventDays: uint32(max(r.EventDays, 0))},
		}
		if r.LogsErasedAt.After(time.Unix(0, 0)) {
			at := r.LogsErasedAt
			out[i].LogsErasedAt = &at
		}
	}
	return out, nil
}

// ExpireEvents deletes the organization's security events and alert history before before (never
// of the last 7 days: the database checks that against its own clock).
func (s *RetentionStore) ExpireEvents(ctx context.Context, org tenant.OrgID, before time.Time) (int64, error) {
	var n int64
	err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		var err error
		n, err = q.ExpireOrganizationEvents(ctx, sqlcdb.ExpireOrganizationEventsParams{OrgID: uuid.UUID(org), Before: before})
		return wrap(err, "expire events")
	})
	return n, err
}

// MarkLogsErased records the last delete request to Loki.
func (s *RetentionStore) MarkLogsErased(ctx context.Context, org tenant.OrgID, at time.Time) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		return wrap(q.MarkLogsErased(ctx, &at), "mark logs erased")
	})
}

func (s *RetentionStore) inOrg(ctx context.Context, org tenant.OrgID, fn func(*sqlcdb.Queries) error) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("organization: %w", err)
	}
	return s.db.InTenant(ctx, scope, fn)
}
