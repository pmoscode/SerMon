package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// OrganizationStore persists organizations for the superadmin.
type OrganizationStore struct{ db *DB }

var _ organization.Store = (*OrganizationStore)(nil)

// NewOrganizationStore returns the PostgreSQL organization store.
func NewOrganizationStore(db *DB) *OrganizationStore { return &OrganizationStore{db: db} }

// List returns all organizations with counts and the storage estimate. Names, member counts and
// quotas come from a SECURITY DEFINER function; nodes, series and events are counted in a tenant
// transaction per organization (numbers only), and the instance's storage is shared by those
// shares (ADR 0029).
func (s *OrganizationStore) List(ctx context.Context) ([]organization.Organization, error) {
	rows, err := s.db.withoutTenant().AdminListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	totals, err := s.db.withoutTenant().AdminStorageTotals(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage totals: %w", err)
	}
	orgs := make([]organization.Organization, 0, len(rows))
	usage := make([]sqlcdb.OrganizationUsageRow, len(rows))
	var series, events int64
	for i, r := range rows {
		scope, err := tenant.ForAllGroups(tenant.OrgID(r.ID))
		if err != nil {
			return nil, fmt.Errorf("organization usage: %w", err)
		}
		err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
			usage[i], err = q.OrganizationUsage(ctx)
			if err != nil {
				return fmt.Errorf("count: %w", err)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("organization usage: %w", err)
		}
		series += usage[i].Series
		events += usage[i].Events
		orgs = append(orgs, organization.Organization{
			ID: tenant.OrgID(r.ID), Name: r.Name, CreatedAt: r.CreatedAt, Members: r.Members, Nodes: usage[i].Nodes,
			Quotas: quota.Quotas{
				MaxNodes: nonNegative(r.MaxNodes), MaxMembers: nonNegative(r.MaxMembers), RetentionCapDays: nonNegative(r.RetentionCapDays),
				SamplesPerSecond: nonNegative(r.SamplesPerSecond), SeriesPerNode: nonNegative(r.SeriesPerNode),
			},
		})
	}
	for i := range orgs {
		orgs[i].StorageBytes = share(totals.MetricBytes, usage[i].Series, series) + share(totals.EventBytes, usage[i].Events, events)
	}
	return orgs, nil
}

// share is total split by part of all (0 without any).
func share(total, part, all int64) int64 {
	if all <= 0 || part <= 0 {
		return 0
	}
	return int64(float64(total) * float64(part) / float64(all))
}

func nonNegative(v int32) uint32 { return uint32(max(v, 0)) }

// quotaArg is a quota for the database: NULL is the instance default.
func quotaArg(v uint32) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(min(v, math.MaxInt32)), Valid: v > 0}
}

func quotaOf(v pgtype.Int4) uint32 {
	if !v.Valid {
		return 0
	}
	return nonNegative(v.Int32)
}

// nameTaken maps the unique index on lower(name).
func nameTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "organizations_name_ci_key" {
		return organization.ErrNameTaken
	}
	return err
}

// Create creates the organization and the invitation of its first admin in one transaction.
func (s *OrganizationStore) Create(ctx context.Context, org organization.Organization, inv invitation.Invitation, hash [32]byte, audit access.AuditEntry) error {
	scope, err := tenant.ForAllGroups(org.ID)
	if err != nil {
		return fmt.Errorf("new organization: %w", err)
	}
	var inviter *uuid.UUID
	if inv.InvitedBy != nil {
		id := uuid.UUID(*inv.InvitedBy)
		inviter = &id
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if _, err := q.CreateOrganization(ctx, sqlcdb.CreateOrganizationParams{ID: uuid.UUID(org.ID), Name: org.Name}); err != nil {
			return fmt.Errorf("insert organization: %w", nameTaken(err))
		}
		err := q.UpsertInvitation(ctx, sqlcdb.UpsertInvitationParams{
			ID: uuid.UUID(inv.ID), Email: inv.Email, GroupID: groupPtr(inv.Group), Role: inv.Role.String(),
			TokenHash: hash[:], InvitedBy: inviter, ExpiresAt: inv.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("invite first admin: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// Rename renames an organization (access.ErrNotFound if it does not exist).
func (s *OrganizationStore) Rename(ctx context.Context, org tenant.OrgID, name string, audit access.AuditEntry) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return access.ErrNotFound
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.RenameOrganization(ctx, name)
		if err != nil {
			return fmt.Errorf("rename organization: %w", nameTaken(err))
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// Update sets name and quotas (access.ErrNotFound if the organization does not exist).
func (s *OrganizationStore) Update(ctx context.Context, org tenant.OrgID, name string, q quota.Quotas, audit func(previous organization.Organization) access.AuditEntry) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return access.ErrNotFound
	}
	return s.db.InTenant(ctx, scope, func(qs *sqlcdb.Queries) error {
		before, err := qs.GetOrganization(ctx, uuid.UUID(org))
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load organization: %w", err)
		}
		old, err := qs.OrganizationQuotas(ctx)
		if err != nil {
			return fmt.Errorf("load quotas: %w", err)
		}
		previous := organization.Organization{ID: org, Name: before.Name, Quotas: quota.Quotas{
			MaxNodes: quotaOf(old.MaxNodes), MaxMembers: quotaOf(old.MaxMembers), RetentionCapDays: quotaOf(old.RetentionCapDays),
			SamplesPerSecond: quotaOf(old.SamplesPerSecond), SeriesPerNode: quotaOf(old.SeriesPerNode),
		}}
		if _, err := qs.UpdateOrganization(ctx, sqlcdb.UpdateOrganizationParams{
			Name: name, MaxNodes: quotaArg(q.MaxNodes), MaxMembers: quotaArg(q.MaxMembers), RetentionCapDays: quotaArg(q.RetentionCapDays),
			SamplesPerSecond: quotaArg(q.SamplesPerSecond), SeriesPerNode: quotaArg(q.SeriesPerNode),
		}); err != nil {
			return fmt.Errorf("update organization: %w", nameTaken(err))
		}
		return appendAudit(ctx, qs, audit(previous))
	})
}
