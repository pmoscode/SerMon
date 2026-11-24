package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// StepUpMaxAge is how long a (re-)authentication allows changes of rights (KONZEPT §7).
const StepUpMaxAge = 15 * time.Minute

// Limits.
const (
	maxGroupName     = 100
	defaultAuditPage = 100
	maxAuditPage     = 500
)

var (
	// ErrForbidden means the policy denies the action.
	ErrForbidden = errors.New("permission denied")
	// ErrNotFound means the object does not exist in the caller's organization.
	ErrNotFound = errors.New("not found")
	// ErrConflict means a node group with that name already exists.
	ErrConflict = errors.New("a node group with this name already exists")
	// ErrLastAdmin means the change would leave the organization without an org-wide admin.
	ErrLastAdmin = errors.New("the organization needs at least one admin for all groups")
	// ErrStepUpRequired means the caller must re-authenticate first.
	ErrStepUpRequired = errors.New("please confirm your identity again")
	// ErrInvalidName means a node group name is empty, too long or contains control characters.
	ErrInvalidName = fmt.Errorf("name must have 1 to %d characters and no control characters", maxGroupName)
	// ErrInvalidRole means the role is not viewer, operator or admin.
	ErrInvalidRole = errors.New("invalid role")
)

// BindingID identifies a role binding.
type BindingID uuid.UUID

// String returns the UUID.
func (id BindingID) String() string { return uuid.UUID(id).String() }

// Group is a node group.
type Group struct {
	ID        tenant.GroupID
	Name      string
	CreatedAt time.Time
}

// RoleBinding is a role of a member on one group or on all groups (Group == nil).
type RoleBinding struct {
	ID    BindingID
	Group *tenant.GroupID
	Role  role.Role
	// FromOIDC: an OIDC group mapping gave it (M3-21).
	FromOIDC bool
}

// Member is a user of the organization with its role bindings.
type Member struct {
	User        identity.UserID
	Email       string
	DisplayName string
	Bindings    []RoleBinding
}

// Grant asks the store to give User the Role on Group (nil = all groups). An existing binding
// for the same user and group is replaced; ID is used for a new binding.
type Grant struct {
	ID    BindingID
	User  identity.UserID
	Group *tenant.GroupID
	Role  role.Role
}

// AuditEntry is written by the store in the same transaction as the change it describes.
// Stores may add facts they determined (e.g. the role of a revoked binding) to Details.
type AuditEntry struct {
	Actor   identity.UserID
	Action  string
	Target  string
	Details map[string]string
}

// AuditRecord is a stored audit entry.
type AuditRecord struct {
	Seq     int64
	At      time.Time
	Actor   *identity.UserID // nil for system actions
	Action  string
	Target  string
	Details map[string]string
}

// Store persists node groups, memberships, role bindings and the audit log. Every method runs
// in one tenant transaction of the scope's organization; mutations write their audit entry in
// the same transaction. Objects of other organizations are reported as ErrNotFound.
type Store interface {
	ListGroups(ctx context.Context, scope tenant.Scope) ([]Group, error)
	CreateGroup(ctx context.Context, scope tenant.Scope, g Group, audit AuditEntry) error
	RenameGroup(ctx context.Context, scope tenant.Scope, id tenant.GroupID, name string, audit AuditEntry) error
	DeleteGroup(ctx context.Context, scope tenant.Scope, id tenant.GroupID, audit AuditEntry) error
	ListMembers(ctx context.Context, scope tenant.Scope) ([]Member, error)
	GrantRole(ctx context.Context, scope tenant.Scope, g Grant, audit AuditEntry) (BindingID, error)
	RevokeRole(ctx context.Context, scope tenant.Scope, id BindingID, audit AuditEntry) error
	RemoveMember(ctx context.Context, scope tenant.Scope, user identity.UserID, audit AuditEntry) error
	ListAudit(ctx context.Context, scope tenant.Scope, beforeSeq int64, limit int) ([]AuditRecord, error)
	// QuotaUsage returns the organization's quotas and what counts against them (M2-32).
	QuotaUsage(ctx context.Context, scope tenant.Scope, now time.Time) (QuotaUsage, error)
}

// QuotaUsage is the state of an organization's quotas, for its admins.
type QuotaUsage struct {
	Quotas          quota.Quotas
	Nodes, Members  int64
	OpenInvitations int64
}

// Caller is the authenticated principal with the time of its last (re-)authentication.
type Caller struct {
	Principal authz.Principal
	StepUpAt  time.Time
}

// NewID returns a time-ordered UUID (v7) for new objects.
func NewID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New() // only fails when the system's random source fails; New panics then
	}
	return id
}

// Service manages node groups, members and roles of an organization.
type Service struct {
	store  Store
	policy authz.Policy
	now    func() time.Time
	newID  func() uuid.UUID
}

// NewService returns a Service. newID generates IDs for new objects (uuid.NewV7 in production).
func NewService(store Store, policy authz.Policy, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, policy: policy, now: now, newID: newID}
}

// ListGroups returns the node groups the caller can see (at least viewer).
func (s *Service) ListGroups(ctx context.Context, c Caller) ([]Group, error) {
	scope, err := authz.ScopeFor(c.Principal, role.Viewer)
	if err != nil {
		return nil, ErrForbidden
	}
	return wrapResult(s.store.ListGroups(ctx, scope))
}

// CreateGroup creates a node group.
func (s *Service) CreateGroup(ctx context.Context, c Caller, name string) (Group, error) {
	scope, err := s.authorize(c, authz.ManageGroups, false)
	if err != nil {
		return Group{}, err
	}
	name, err = groupName(name)
	if err != nil {
		return Group{}, err
	}
	g := Group{ID: tenant.GroupID(s.newID()), Name: name, CreatedAt: s.now()}
	if err := s.store.CreateGroup(ctx, scope, g, s.audit(c, "group.create", "group:"+g.ID.String(), "name", name)); err != nil {
		return Group{}, fmt.Errorf("create group: %w", err)
	}
	return g, nil
}

// RenameGroup renames a node group.
func (s *Service) RenameGroup(ctx context.Context, c Caller, id tenant.GroupID, name string) error {
	scope, err := s.authorize(c, authz.ManageGroups, false)
	if err != nil {
		return err
	}
	if name, err = groupName(name); err != nil {
		return err
	}
	return wrap(s.store.RenameGroup(ctx, scope, id, name, s.audit(c, "group.rename", "group:"+id.String(), "name", name)), "rename group")
}

// DeleteGroup deletes a node group and its role bindings (a change of rights: step-up).
func (s *Service) DeleteGroup(ctx context.Context, c Caller, id tenant.GroupID) error {
	scope, err := s.authorize(c, authz.ManageGroups, true)
	if err != nil {
		return err
	}
	return wrap(s.store.DeleteGroup(ctx, scope, id, s.audit(c, "group.delete", "group:"+id.String())), "delete group")
}

// QuotaUsage returns the quotas the superadmin set and how much of them is used (org admin).
func (s *Service) QuotaUsage(ctx context.Context, c Caller) (QuotaUsage, error) {
	scope, err := s.authorize(c, authz.ManageMembers, false)
	if err != nil {
		return QuotaUsage{}, err
	}
	return wrapResult(s.store.QuotaUsage(ctx, scope, s.now()))
}

// ListMembers returns the members of the organization with their roles.
func (s *Service) ListMembers(ctx context.Context, c Caller) ([]Member, error) {
	scope, err := s.authorize(c, authz.ManageMembers, false)
	if err != nil {
		return nil, err
	}
	return wrapResult(s.store.ListMembers(ctx, scope))
}

// GrantRole gives a member a role on one group or on all groups (group == nil).
func (s *Service) GrantRole(ctx context.Context, c Caller, user identity.UserID, group *tenant.GroupID, r role.Role) (BindingID, error) {
	scope, err := s.authorize(c, authz.ManageMembers, true)
	if err != nil {
		return BindingID{}, err
	}
	if !r.Valid() {
		return BindingID{}, ErrInvalidRole
	}
	groupName := "*"
	if group != nil {
		groupName = group.String()
	}
	g := Grant{ID: BindingID(s.newID()), User: user, Group: group, Role: r}
	id, err := s.store.GrantRole(ctx, scope, g, s.audit(c, "role.grant", "user:"+user.String(), "role", r.String(), "group", groupName))
	if err != nil {
		return BindingID{}, fmt.Errorf("grant role: %w", err)
	}
	return id, nil
}

// RevokeRole removes a role binding.
func (s *Service) RevokeRole(ctx context.Context, c Caller, id BindingID) error {
	scope, err := s.authorize(c, authz.ManageMembers, true)
	if err != nil {
		return err
	}
	return wrap(s.store.RevokeRole(ctx, scope, id, s.audit(c, "role.revoke", "binding:"+id.String())), "revoke role")
}

// RemoveMember removes a user from the organization (with all role bindings).
func (s *Service) RemoveMember(ctx context.Context, c Caller, user identity.UserID) error {
	scope, err := s.authorize(c, authz.ManageMembers, true)
	if err != nil {
		return err
	}
	return wrap(s.store.RemoveMember(ctx, scope, user, s.audit(c, "member.remove", "user:"+user.String())), "remove member")
}

// ListAudit returns audit entries older than beforeSeq (0 = newest), newest first.
func (s *Service) ListAudit(ctx context.Context, c Caller, beforeSeq int64, limit int) ([]AuditRecord, error) {
	scope, err := s.authorize(c, authz.ViewAuditLog, false)
	if err != nil {
		return nil, err
	}
	switch {
	case limit <= 0:
		limit = defaultAuditPage
	case limit > maxAuditPage:
		limit = maxAuditPage
	}
	return wrapResult(s.store.ListAudit(ctx, scope, beforeSeq, limit))
}

func (s *Service) authorize(c Caller, action authz.Action, stepUp bool) (tenant.Scope, error) {
	return Authorize(s.policy, c, action, stepUp, s.now())
}

// Authorize checks an organization-wide action (and, for changes of rights, a step-up within
// StepUpMaxAge) and returns the scope of the caller's organization.
func Authorize(policy authz.Policy, c Caller, action authz.Action, stepUp bool, now time.Time) (tenant.Scope, error) {
	p := c.Principal
	if !policy.Allowed(p, action, authz.InOrg(p.Org)) {
		return tenant.Scope{}, ErrForbidden
	}
	if stepUp && now.Sub(c.StepUpAt) > StepUpMaxAge {
		return tenant.Scope{}, ErrStepUpRequired
	}
	scope, err := tenant.ForAllGroups(p.Org)
	if err != nil {
		return tenant.Scope{}, ErrForbidden
	}
	return scope, nil
}

func (s *Service) audit(c Caller, action, target string, kv ...string) AuditEntry {
	details := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		details[kv[i]] = kv[i+1]
	}
	return AuditEntry{Actor: c.Principal.User, Action: action, Target: target, Details: details}
}

func groupName(name string) (string, error) {
	name, ok := naming.Clean(name, maxGroupName)
	if !ok {
		return "", ErrInvalidName
	}
	return name, nil
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}

func wrapResult[T any](v T, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, fmt.Errorf("access store: %w", err)
	}
	return v, nil
}
