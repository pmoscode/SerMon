// Package oidcmapping maps groups of the OIDC provider to roles of an organization (M3-21, ADR
// 0033): its admins keep the mappings, every sign-in reconciles the roles that came from them
// and, with just-in-time provisioning, creates the account and the membership. Roles given by
// hand stay untouched.
package oidcmapping

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Bounds of the mappings.
const (
	maxDisplayName = 200
	MaxGroupName   = 255
	MaxMappings    = 200
	// MaxClaimGroups bounds the groups of a sign-in that are compared.
	MaxClaimGroups = 1000
)

var (
	// ErrInvalid means a mapping the service doesn't take.
	ErrInvalid = errors.New("invalid mapping")
	// ErrExists means the organization maps the provider's group to the node group already.
	ErrExists = errors.New("the group is mapped to this node group already")
	// ErrTooMany means the organization has MaxMappings.
	ErrTooMany = fmt.Errorf("at most %d mappings per organization", MaxMappings)
	// ErrEmailTaken means an account has the e-mail of the identity: it must be linked first.
	ErrEmailTaken = errors.New("an account with this e-mail exists; link the identity to it")
)

// ID identifies a mapping.
type ID uuid.UUID

// String returns the UUID.
func (id ID) String() string { return uuid.UUID(id).String() }

// Mapping gives the members of a provider group a role on a node group (nil: all groups).
type Mapping struct {
	ID        ID
	OIDCGroup string
	Group     *tenant.GroupID
	Role      role.Role
	CreatedAt time.Time
}

// Binding is a role binding of the signed-in user; FromOIDC marks one a mapping gave.
type Binding struct {
	ID       uuid.UUID
	Group    *tenant.GroupID
	Role     role.Role
	FromOIDC bool
}

// ChangeKind is what a reconciliation does to a binding.
type ChangeKind int

// Kinds of changes.
const (
	Add ChangeKind = iota + 1
	Update
	Remove
)

// Change is one step of a reconciliation.
type Change struct {
	Kind    ChangeKind
	Binding uuid.UUID // Update, Remove
	Group   *tenant.GroupID
	Role    role.Role // Add, Update: the new role
}

func key(g *tenant.GroupID) string {
	if g == nil {
		return "*"
	}
	return g.String()
}

// Desired returns the role per node group ("*": all groups) the provider groups give: the
// highest of every matching mapping.
func Desired(mappings []Mapping, groups []string) map[string]Mapping {
	if len(groups) > MaxClaimGroups {
		groups = groups[:MaxClaimGroups]
	}
	member := make(map[string]bool, len(groups))
	for _, g := range groups {
		member[g] = true
	}
	out := map[string]Mapping{}
	for _, m := range mappings {
		if !member[m.OIDCGroup] {
			continue
		}
		k := key(m.Group)
		if cur, ok := out[k]; !ok || m.Role > cur.Role {
			out[k] = m
		}
	}
	return out
}

// Plan returns the changes that bring the user's bindings to desired: bindings given by hand
// stay, those of mappings follow them.
func Plan(existing []Binding, desired map[string]Mapping) []Change {
	var out []Change
	have := make(map[string]Binding, len(existing))
	for _, b := range existing {
		have[key(b.Group)] = b
	}
	for k, m := range desired {
		b, ok := have[k]
		switch {
		case !ok:
			out = append(out, Change{Kind: Add, Group: m.Group, Role: m.Role})
		case b.FromOIDC && b.Role != m.Role:
			out = append(out, Change{Kind: Update, Binding: b.ID, Group: b.Group, Role: m.Role})
		}
	}
	for k, b := range have {
		if _, ok := desired[k]; b.FromOIDC && !ok {
			out = append(out, Change{Kind: Remove, Binding: b.ID, Group: b.Group, Role: b.Role})
		}
	}
	slices.SortFunc(out, func(a, b Change) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), strings.Compare(key(a.Group), key(b.Group)))
	})
	return out
}

// OrgResult is what a reconciliation did in an organization.
type OrgResult struct {
	Joined  bool
	Applied []Change
	// Kept are changes left out because they would remove the last org-wide admin.
	Kept int
	// Skipped names why nothing changed: "locked", "not_member", "quota".
	Skipped string
}

// Store keeps the mappings and applies reconciliations.
type Store interface {
	List(ctx context.Context, scope tenant.Scope) ([]Mapping, error)
	Create(ctx context.Context, scope tenant.Scope, m Mapping, audit access.AuditEntry) error
	Delete(ctx context.Context, scope tenant.Scope, id ID, audit access.AuditEntry) error
	// MappedOrganizations returns the unlocked organizations with a mapping of one of the groups
	// (instance-wide).
	MappedOrganizations(ctx context.Context, groups []string) ([]tenant.OrgID, error)
	// Reconcile runs in one transaction of the organization: plan gets its mappings, the user's
	// bindings and membership and returns the changes; the store joins (if join and the quota
	// allows), applies them without removing the last org-wide admin and audits each.
	Reconcile(ctx context.Context, scope tenant.Scope, user identity.UserID, join bool,
		plan func(mappings []Mapping, existing []Binding) []Change) (OrgResult, error)
	// Provision creates an account with the identity (instance audit log); ErrEmailTaken if an
	// account has the e-mail.
	Provision(ctx context.Context, email, name string, id oidc.Identity) (identity.UserID, error)
}

// Memberships lists the unlocked organizations of a user (the credential store).
type Memberships interface {
	Organizations(ctx context.Context, user identity.UserID) ([]auth.Organization, error)
}

// Service keeps the mappings and reconciles sign-ins.
type Service struct {
	store   Store
	members Memberships
	policy  authz.Policy
	now     func() time.Time
	newID   func() uuid.UUID
	logger  *slog.Logger
}

// NewService returns the service.
func NewService(store Store, members Memberships, policy authz.Policy, now func() time.Time, newID func() uuid.UUID, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, members: members, policy: policy, now: now, newID: newID, logger: logger}
}

var _ oidc.Provisioner = (*Service)(nil)

// List returns the mappings of the caller's organization (admins for all groups).
func (s *Service) List(ctx context.Context, c access.Caller) ([]Mapping, error) {
	scope, err := access.Authorize(s.policy, c, authz.ManageMembers, false, s.now())
	if err != nil {
		return nil, err //nolint:wrapcheck // access errors
	}
	list, err := s.store.List(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list oidc mappings: %w", err)
	}
	return list, nil
}

func validGroupName(s string) bool {
	return s != "" && len(s) <= MaxGroupName && utf8.ValidString(s) && strings.TrimSpace(s) == s &&
		!strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// Create maps a provider group to a role (step-up, audit).
func (s *Service) Create(ctx context.Context, c access.Caller, oidcGroup string, group *tenant.GroupID, r role.Role) (Mapping, error) {
	scope, err := access.Authorize(s.policy, c, authz.ManageMembers, true, s.now())
	if err != nil {
		return Mapping{}, err //nolint:wrapcheck // access errors
	}
	if !validGroupName(oidcGroup) {
		return Mapping{}, fmt.Errorf("%w: the provider group needs 1 to %d bytes without control characters or outer spaces", ErrInvalid, MaxGroupName)
	}
	if !r.Valid() {
		return Mapping{}, access.ErrInvalidRole
	}
	m := Mapping{ID: ID(s.newID()), OIDCGroup: oidcGroup, Group: group, Role: r, CreatedAt: s.now()}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "oidc_mapping.create", Target: "oidc_mapping:" + m.ID.String(),
		Details: map[string]string{"oidc_group": oidcGroup, "group": key(group), "role": r.String()},
	}
	if err := s.store.Create(ctx, scope, m, audit); err != nil {
		return Mapping{}, fmt.Errorf("create oidc mapping: %w", err)
	}
	return m, nil
}

// Delete removes a mapping (step-up, audit); the roles it gave go with the members' next sign-in.
func (s *Service) Delete(ctx context.Context, c access.Caller, id ID) error {
	scope, err := access.Authorize(s.policy, c, authz.ManageMembers, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // access errors
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "oidc_mapping.delete", Target: "oidc_mapping:" + id.String()}
	if err := s.store.Delete(ctx, scope, id, audit); err != nil {
		return fmt.Errorf("delete oidc mapping: %w", err)
	}
	return nil
}

// Reconcile implements oidc.Provisioner: in every organization the user is a member of, and with
// join every unlocked one whose mappings match, the roles follow the provider groups.
func (s *Service) Reconcile(ctx context.Context, user identity.UserID, groups []string, join bool) error {
	member, err := s.members.Organizations(ctx, user)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	orgs := make([]tenant.OrgID, 0, len(member))
	for _, o := range member {
		orgs = append(orgs, o.ID)
	}
	if join && len(groups) > 0 {
		mapped, err := s.store.MappedOrganizations(ctx, capped(groups))
		if err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		for _, o := range mapped {
			if !slices.Contains(orgs, o) {
				orgs = append(orgs, o)
			}
		}
	}
	for _, org := range orgs {
		scope, err := tenant.ForAllGroups(org)
		if err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		res, err := s.store.Reconcile(ctx, scope, user, join, func(mappings []Mapping, existing []Binding) []Change {
			return Plan(existing, Desired(mappings, groups))
		})
		if err != nil {
			return fmt.Errorf("reconcile %s: %w", org, err)
		}
		if res.Kept > 0 {
			s.logger.WarnContext(ctx, "oidc: kept the last org-wide admin role against the provider's groups", "org", org.String(), "user", user.String())
		}
		if res.Skipped == "quota" {
			s.logger.WarnContext(ctx, "oidc: no membership by the mapping, the organization's member quota is reached", "org", org.String())
		}
	}
	return nil
}

func capped(groups []string) []string {
	if len(groups) > MaxClaimGroups {
		return groups[:MaxClaimGroups]
	}
	return groups
}

// Provision implements oidc.Provisioner: an account for an identity nobody has, if its e-mail
// is verified and a mapping of an unlocked organization matches its groups.
func (s *Service) Provision(ctx context.Context, c oidc.Claims) (identity.UserID, bool, error) {
	if len(c.Groups) == 0 || c.Email == "" || !c.EmailVerified {
		return identity.UserID{}, false, nil
	}
	mapped, err := s.store.MappedOrganizations(ctx, capped(c.Groups))
	if err != nil {
		return identity.UserID{}, false, fmt.Errorf("provision: %w", err)
	}
	if len(mapped) == 0 {
		return identity.UserID{}, false, nil
	}
	// The provider's name is shown to admins and members: the rules of every other name apply
	// (no control, bidi or invisible characters, M3-28); else the local part of the e-mail.
	name, ok := naming.Clean(c.Name, maxDisplayName)
	if !ok {
		local, _, _ := strings.Cut(c.Email, "@")
		if name, ok = naming.Clean(local, maxDisplayName); !ok {
			name = "SSO"
		}
	}
	user, err := s.store.Provision(ctx, c.Email, name, oidc.Identity{Issuer: c.Issuer, Subject: c.Subject, Email: c.Email})
	if errors.Is(err, ErrEmailTaken) {
		s.logger.WarnContext(ctx, "oidc: no account provisioned, the e-mail has one (link the identity to it)")
		return identity.UserID{}, false, nil
	}
	if err != nil {
		return identity.UserID{}, false, fmt.Errorf("provision: %w", err)
	}
	return user, true, nil
}
