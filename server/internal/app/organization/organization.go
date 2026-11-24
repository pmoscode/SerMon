// Package organization lets the superadmin manage organizations (KONZEPT §7). The superadmin
// sees only names and member counts – tenant data stays invisible without a membership. The
// first admin of a new organization is invited by link (ADR 0008).
package organization

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

const maxName = 200

// ErrInvalidName means the organization name is empty, too long or contains control characters.
var ErrInvalidName = fmt.Errorf("name must have 1 to %d characters and no control characters", maxName)

// ErrNameTaken means another organization has the name (regardless of case).
var ErrNameTaken = errors.New("another organization has this name")

// Organization is an organization as the superadmin sees it: counts and sizes, no tenant data.
type Organization struct {
	ID        tenant.OrgID
	Name      string
	CreatedAt time.Time
	Members   int64
	Nodes     int64
	// StorageBytes estimates its metrics and security events: the instance's totals shared by its
	// share of the series and of the events (ADR 0029, M2-32).
	StorageBytes int64
	Quotas       quota.Quotas
	// LockedAt and DeletionDueAt are set while it is locked and scheduled for deletion (M2-33).
	LockedAt, DeletionDueAt *time.Time
}

// Store persists organizations; Create, Rename and Update write the audit entry into the
// organization's log in the same transaction. Duplicate names are ErrNameTaken.
type Store interface {
	List(ctx context.Context) ([]Organization, error)
	Create(ctx context.Context, org Organization, firstAdmin invitation.Invitation, hash [32]byte, audit access.AuditEntry) error
	Rename(ctx context.Context, org tenant.OrgID, name string, audit access.AuditEntry) error
	// Update sets name and quotas; audit builds the entry from the previous state.
	Update(ctx context.Context, org tenant.OrgID, name string, q quota.Quotas, audit func(previous Organization) access.AuditEntry) error
}

// Service manages organizations.
type Service struct {
	store   Store
	policy  authz.Policy
	now     func() time.Time
	newID   func() uuid.UUID
	mailer  invitation.Mailer
	baseURL string
}

// Option configures a Service.
type Option func(*Service)

// WithMailer sends the first admin's invitation link by mail instead of returning it
// (production, M1-39); baseURL is the origin of the UI.
func WithMailer(mailer invitation.Mailer, baseURL string) Option {
	return func(s *Service) { s.mailer, s.baseURL = mailer, baseURL }
}

// NewService returns a Service.
func NewService(store Store, policy authz.Policy, now func() time.Time, newID func() uuid.UUID, options ...Option) *Service {
	s := &Service{store: store, policy: policy, now: now, newID: newID}
	for _, option := range options {
		option(s)
	}
	return s
}

// List returns all organizations (superadmin).
func (s *Service) List(ctx context.Context, c access.Caller) ([]Organization, error) {
	if err := s.authorize(c, false); err != nil {
		return nil, err
	}
	orgs, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	return orgs, nil
}

// Create creates an organization and invites its first org-wide admin; it returns the link
// token (shown once) – or "" if the link went out by mail (WithMailer). It hands out admin
// rights, so it needs a step-up.
func (s *Service) Create(ctx context.Context, c access.Caller, name, adminEmail string) (Organization, string, error) {
	if err := s.authorize(c, true); err != nil {
		return Organization{}, "", err
	}
	name, ok := naming.Clean(name, maxName)
	if !ok {
		return Organization{}, "", ErrInvalidName
	}
	email, ok := identity.NormalizeEmail(adminEmail)
	if !ok {
		return Organization{}, "", invitation.ErrInvalidEmail
	}
	token, hash, err := invitation.NewToken()
	if err != nil {
		return Organization{}, "", fmt.Errorf("first admin invitation: %w", err)
	}
	now := s.now()
	inviter := c.Principal.User
	org := Organization{ID: tenant.OrgID(s.newID()), Name: name, CreatedAt: now}
	inv := invitation.Invitation{
		ID: invitation.ID(s.newID()), Email: email, Role: role.Admin, InvitedBy: &inviter, CreatedAt: now, ExpiresAt: now.Add(invitation.Lifetime),
	}
	audit := access.AuditEntry{
		Actor: inviter, Action: "org.create", Target: "org:" + org.ID.String(),
		Details: map[string]string{"name": name, "admin_email": email},
	}
	if err := s.store.Create(ctx, org, inv, hash, audit); err != nil {
		return Organization{}, "", fmt.Errorf("create organization: %w", err)
	}
	if s.mailer == nil {
		return org, token, nil
	}
	err = invitation.SendMail(ctx, s.mailer, s.baseURL, token, invitation.MailFacts{
		To: email, Organization: name, Role: role.Admin, ExpiresAt: inv.ExpiresAt,
	})
	if err != nil {
		return org, "", err //nolint:wrapcheck // ErrMailFailed for the handler
	}
	return org, "", nil
}

// Rename renames an organization; like every change of an organization it needs a step-up
// (M3-25): members recognize their organization by its name.
func (s *Service) Rename(ctx context.Context, c access.Caller, id tenant.OrgID, name string) error {
	if err := s.authorize(c, true); err != nil {
		return err
	}
	name, ok := naming.Clean(name, maxName)
	if !ok {
		return ErrInvalidName
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "org.rename", Target: "org:" + id.String(),
		Details: map[string]string{"name": name},
	}
	if err := s.store.Rename(ctx, id, name, audit); err != nil {
		return fmt.Errorf("rename organization: %w", err)
	}
	return nil
}

// Update sets the name and the quotas of an organization (M2-32). Quotas bound what an
// organization may add, so changing them needs a step-up; lowering one deletes nothing.
func (s *Service) Update(ctx context.Context, c access.Caller, id tenant.OrgID, name string, q quota.Quotas) error {
	if err := s.authorize(c, true); err != nil {
		return err
	}
	name, ok := naming.Clean(name, maxName)
	if !ok {
		return ErrInvalidName
	}
	if err := q.Validate(); err != nil {
		return fmt.Errorf("update organization: %w", err)
	}
	err := s.store.Update(ctx, id, name, q, func(previous Organization) access.AuditEntry {
		return access.AuditEntry{
			Actor: c.Principal.User, Action: "org.update", Target: "org:" + id.String(),
			Details: map[string]string{
				"name": name, "previous_name": previous.Name, "quotas": q.String(), "previous_quotas": previous.Quotas.String(),
			},
		}
	})
	if err != nil {
		return fmt.Errorf("update organization: %w", err)
	}
	return nil
}

var errNoSuperadmin = errors.New("superadmin required")

func (s *Service) authorize(c access.Caller, stepUp bool) error {
	if !s.policy.AllowedInstance(c.Principal, authz.ManageOrganizations) {
		return fmt.Errorf("%w: %w", access.ErrForbidden, errNoSuperadmin)
	}
	if stepUp && s.now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	return nil
}
