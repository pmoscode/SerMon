package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// InvitationStore persists invitations (ADR 0008).
type InvitationStore struct{ db *DB }

var _ invitation.Store = (*InvitationStore)(nil)

// NewInvitationStore returns the PostgreSQL invitation store.
func NewInvitationStore(db *DB) *InvitationStore { return &InvitationStore{db: db} }

// Create stores an invitation; an open invitation for the same address is replaced.
func (s *InvitationStore) Create(ctx context.Context, scope tenant.Scope, inv invitation.Invitation, hash [32]byte, audit access.AuditEntry) error {
	var inviter *uuid.UUID
	if inv.InvitedBy != nil {
		id := uuid.UUID(*inv.InvitedBy)
		inviter = &id
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := admitInvitation(ctx, q, inv); err != nil {
			return err
		}
		err := q.UpsertInvitation(ctx, sqlcdb.UpsertInvitationParams{
			ID: uuid.UUID(inv.ID), Email: inv.Email, GroupID: groupPtr(inv.Group), Role: inv.Role.String(),
			TokenHash: hash[:], InvitedBy: inviter, ExpiresAt: inv.ExpiresAt,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return access.ErrNotFound // group of another organization or deleted
		}
		if err != nil {
			return fmt.Errorf("store invitation: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// List returns the organization's open invitations, newest first.
func (s *InvitationStore) List(ctx context.Context, scope tenant.Scope) ([]invitation.Invitation, error) {
	var invs []invitation.Invitation
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListInvitations(ctx)
		if err != nil {
			return fmt.Errorf("list invitations: %w", err)
		}
		for _, r := range rows {
			parsed, err := role.Parse(r.Role)
			if err != nil {
				return fmt.Errorf("invitation %s: %w", r.ID, err)
			}
			inv := invitation.Invitation{
				ID: invitation.ID(r.ID), Email: r.Email, Group: groupID(r.GroupID), Role: parsed,
				CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
			}
			if r.InvitedBy != nil {
				by := identity.UserID(*r.InvitedBy)
				inv.InvitedBy = &by
			}
			invs = append(invs, inv)
		}
		return nil
	})
	return invs, err
}

// Revoke deletes an invitation of the organization.
func (s *InvitationStore) Revoke(ctx context.Context, scope tenant.Scope, id invitation.ID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteInvitation(ctx, uuid.UUID(id))
		if err != nil {
			return fmt.Errorf("delete invitation: %w", err)
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// Lookup finds a valid invitation by token hash (SECURITY DEFINER, no tenant).
func (s *InvitationStore) Lookup(ctx context.Context, hash [32]byte, now time.Time) (invitation.Pending, bool, error) {
	r, err := s.db.withoutTenant().InvitationByToken(ctx, sqlcdb.InvitationByTokenParams{TokenHash: hash[:], At: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return invitation.Pending{}, false, nil
	}
	if err != nil {
		return invitation.Pending{}, false, fmt.Errorf("look up invitation: %w", err)
	}
	parsed, err := role.Parse(r.Role)
	if err != nil {
		return invitation.Pending{}, false, fmt.Errorf("invitation %s: %w", r.ID, err)
	}
	p := invitation.Pending{
		ID: invitation.ID(r.ID), Org: tenant.OrgID(r.OrgID), OrgName: r.OrgName, Email: r.Email, Role: parsed, ExpiresAt: r.ExpiresAt,
	}
	if r.GroupID != uuid.Nil {
		p.Group = groupID(&r.GroupID)
	}
	return p, true, nil
}

// AcceptNewAccount creates the account for the invited address and joins it. If the address
// already has an account, nothing changes (the invitation stays) and ErrInvalidInvitation is returned.
func (s *InvitationStore) AcceptNewAccount(ctx context.Context, p invitation.Pending, a invitation.NewAccount, binding uuid.UUID, now time.Time, audit access.AuditEntry) error {
	return s.accept(ctx, p, func(q *sqlcdb.Queries) error {
		existing, err := q.FindUserByEmail(ctx, p.Email)
		if err != nil {
			return fmt.Errorf("check address: %w", err)
		}
		if existing != uuid.Nil {
			return invitation.ErrInvalidInvitation
		}
		user := uuid.UUID(a.User)
		err = q.CreateUser(ctx, sqlcdb.CreateUserParams{ID: user, Email: p.Email, DisplayName: a.DisplayName})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return invitation.ErrInvalidInvitation // created concurrently
		}
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if err := q.SetPassword(ctx, sqlcdb.SetPasswordParams{UserID: user, PasswordHash: a.PasswordHash, ChangedAt: now}); err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		return s.join(ctx, q, p, a.User, binding, audit)
	})
}

// AcceptExisting joins an existing user (whose address the service checked).
func (s *InvitationStore) AcceptExisting(ctx context.Context, p invitation.Pending, user identity.UserID, binding uuid.UUID, audit access.AuditEntry) error {
	return s.accept(ctx, p, func(q *sqlcdb.Queries) error { return s.join(ctx, q, p, user, binding, audit) })
}

// accept consumes the invitation in the organization's tenant transaction: a concurrent second
// acceptance finds no row and fails.
func (s *InvitationStore) accept(ctx context.Context, p invitation.Pending, fn func(q *sqlcdb.Queries) error) error {
	scope, err := tenant.ForAllGroups(p.Org)
	if err != nil {
		return invitation.ErrInvalidInvitation
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteInvitation(ctx, uuid.UUID(p.ID))
		if err != nil {
			return fmt.Errorf("consume invitation: %w", err)
		}
		if n == 0 {
			return invitation.ErrInvalidInvitation
		}
		return fn(q)
	})
}

func (s *InvitationStore) join(ctx context.Context, q *sqlcdb.Queries, p invitation.Pending, user identity.UserID, binding uuid.UUID, audit access.AuditEntry) error {
	if err := admitMember(ctx, q, user); err != nil {
		return err
	}
	if err := q.AddMembershipIfMissing(ctx, uuid.UUID(user)); err != nil {
		return fmt.Errorf("add membership: %w", err)
	}
	err := q.GrantRoleKeepHigher(ctx, sqlcdb.GrantRoleKeepHigherParams{
		ID: binding, UserID: uuid.UUID(user), GroupID: groupPtr(p.Group), Role: p.Role.String(),
	})
	if err != nil {
		return fmt.Errorf("grant role: %w", err)
	}
	return appendAudit(ctx, q, audit)
}

func groupPtr(g *tenant.GroupID) *uuid.UUID {
	if g == nil {
		return nil
	}
	id := uuid.UUID(*g)
	return &id
}

func groupID(g *uuid.UUID) *tenant.GroupID {
	if g == nil {
		return nil
	}
	id := tenant.GroupID(*g)
	return &id
}

// admitInvitation applies the member quota (M2-32) to a new invitation: members and the other
// open invitations count; inviting the same address again replaces its invitation.
func admitInvitation(ctx context.Context, q *sqlcdb.Queries, inv invitation.Invitation) error {
	limits, err := q.OrganizationQuotasForUpdate(ctx)
	if err != nil {
		return fmt.Errorf("lock organization quotas: %w", err)
	}
	limit := quotaOf(limits.MaxMembers)
	if limit == 0 {
		return nil
	}
	members, err := q.CountMembers(ctx)
	if err != nil {
		return fmt.Errorf("count members: %w", err)
	}
	open, err := q.CountOpenInvitations(ctx, sqlcdb.CountOpenInvitationsParams{Now: inv.CreatedAt, Email: inv.Email})
	if err != nil {
		return fmt.Errorf("count invitations: %w", err)
	}
	if quota.Reached(limit, members+open) {
		return quota.Exceeded{Kind: quota.Members, Limit: limit}
	}
	return nil
}

// admitMember applies the member quota when an invitation is accepted (it may have been lowered
// since): an existing member only gets a role, anyone else needs room. The invitation stays.
func admitMember(ctx context.Context, q *sqlcdb.Queries, user identity.UserID) error {
	limits, err := q.OrganizationQuotasForUpdate(ctx)
	if err != nil {
		return fmt.Errorf("lock organization quotas: %w", err)
	}
	limit := quotaOf(limits.MaxMembers)
	if limit == 0 {
		return nil
	}
	member, err := q.IsMember(ctx, uuid.UUID(user))
	if err != nil {
		return fmt.Errorf("check membership: %w", err)
	}
	if member {
		return nil
	}
	members, err := q.CountMembers(ctx)
	if err != nil {
		return fmt.Errorf("count members: %w", err)
	}
	if quota.Reached(limit, members) {
		return quota.Exceeded{Kind: quota.Members, Limit: limit}
	}
	return nil
}
