package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// OrganizationStore persists organizations for the superadmin.
type OrganizationStore struct{ db *DB }

var _ organization.Store = (*OrganizationStore)(nil)

// NewOrganizationStore returns the PostgreSQL organization store.
func NewOrganizationStore(db *DB) *OrganizationStore { return &OrganizationStore{db: db} }

// List returns all organizations with their member counts (SECURITY DEFINER, no tenant data).
func (s *OrganizationStore) List(ctx context.Context) ([]organization.Organization, error) {
	rows, err := s.db.withoutTenant().AdminListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	orgs := make([]organization.Organization, 0, len(rows))
	for _, r := range rows {
		orgs = append(orgs, organization.Organization{ID: tenant.OrgID(r.ID), Name: r.Name, CreatedAt: r.CreatedAt, Members: r.Members})
	}
	return orgs, nil
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
			return fmt.Errorf("insert organization: %w", err)
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
			return fmt.Errorf("rename organization: %w", err)
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}
