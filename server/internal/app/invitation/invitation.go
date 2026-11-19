// Package invitation lets org admins invite members by a single-use link (ADR 0008). The link
// token has 256 bits, only its SHA-256 is stored, it is valid for 72 hours. Invitations never
// reveal whether an e-mail address already has an account.
package invitation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Lifetime is how long an invitation link is valid.
const Lifetime = 72 * time.Hour

const (
	tokenBytes  = 32
	tokenLength = 43
	maxName     = 200
)

var (
	// ErrInvalidInvitation means the link is unknown, expired, used, revoked or not meant for the
	// caller; also returned when a new account cannot be created for the address (not distinguished).
	ErrInvalidInvitation = errors.New("invalid or expired invitation")
	// ErrInvalidEmail means the e-mail address is malformed.
	ErrInvalidEmail = errors.New("invalid e-mail address")
	// ErrInvalidName means the display name is empty, too long or contains control characters.
	ErrInvalidName = fmt.Errorf("name must have 1 to %d characters and no control characters", maxName)
)

// ID identifies an invitation.
type ID uuid.UUID

// String returns the UUID.
func (id ID) String() string { return uuid.UUID(id).String() }

// Invitation is an open invitation of an organization.
type Invitation struct {
	ID        ID
	Email     string
	Group     *tenant.GroupID // nil: all groups
	Role      role.Role
	InvitedBy *identity.UserID
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Pending is a valid invitation found by its token.
type Pending struct {
	ID        ID
	Org       tenant.OrgID
	OrgName   string
	Email     string
	Group     *tenant.GroupID
	Role      role.Role
	ExpiresAt time.Time
}

// NewAccount is the account created when accepting without an existing one.
type NewAccount struct {
	User         identity.UserID
	DisplayName  string
	PasswordHash string
}

// Store persists invitations. Create, Revoke and both Accept methods write the audit entry in
// the same transaction. Accepting consumes the invitation, adds the membership and grants the role.
type Store interface {
	Create(ctx context.Context, scope tenant.Scope, inv Invitation, hash [32]byte, audit access.AuditEntry) error
	List(ctx context.Context, scope tenant.Scope) ([]Invitation, error)
	Revoke(ctx context.Context, scope tenant.Scope, id ID, audit access.AuditEntry) error
	Lookup(ctx context.Context, hash [32]byte, now time.Time) (Pending, bool, error)
	// AcceptNewAccount returns ErrInvalidInvitation when the address already has an account or
	// the invitation is gone (the invitation stays usable in the first case).
	AcceptNewAccount(ctx context.Context, p Pending, a NewAccount, binding uuid.UUID, now time.Time, audit access.AuditEntry) error
	AcceptExisting(ctx context.Context, p Pending, user identity.UserID, binding uuid.UUID, audit access.AuditEntry) error
}

// Profiles reads the e-mail address of a user (auth.CredentialStore).
type Profiles interface {
	Profile(ctx context.Context, user identity.UserID) (auth.Profile, bool, error)
}

// Hasher hashes passwords (password.Hasher).
type Hasher interface {
	Hash(pw string) (string, error)
}

// Service manages invitations.
type Service struct {
	store    Store
	profiles Profiles
	policy   authz.Policy
	hasher   Hasher
	now      func() time.Time
	newID    func() uuid.UUID
}

// NewService returns a Service.
func NewService(store Store, profiles Profiles, policy authz.Policy, hasher Hasher, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, profiles: profiles, policy: policy, hasher: hasher, now: now, newID: newID}
}

// Invite creates an invitation (replacing an open one for the same address) and returns the
// link token. Inviting grants a role, so it needs a step-up like other changes of rights.
func (s *Service) Invite(ctx context.Context, c access.Caller, email string, group *tenant.GroupID, r role.Role) (string, Invitation, error) {
	scope, err := s.authorize(c, true)
	if err != nil {
		return "", Invitation{}, err
	}
	email, ok := identity.NormalizeEmail(email)
	if !ok {
		return "", Invitation{}, ErrInvalidEmail
	}
	if !r.Valid() {
		return "", Invitation{}, access.ErrInvalidRole
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Invitation{}, fmt.Errorf("generate invitation token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	inviter := c.Principal.User
	inv := Invitation{ID: ID(s.newID()), Email: email, Group: group, Role: r, InvitedBy: &inviter, CreatedAt: now, ExpiresAt: now.Add(Lifetime)}
	audit := access.AuditEntry{
		Actor: inviter, Action: "member.invite", Target: "invitation:" + inv.ID.String(),
		Details: map[string]string{"email": email, "role": r.String(), "group": groupName(group)},
	}
	if err := s.store.Create(ctx, scope, inv, sha256.Sum256([]byte(token)), audit); err != nil {
		return "", Invitation{}, fmt.Errorf("create invitation: %w", err)
	}
	return token, inv, nil
}

// List returns the open invitations of the caller's organization.
func (s *Service) List(ctx context.Context, c access.Caller) ([]Invitation, error) {
	scope, err := s.authorize(c, false)
	if err != nil {
		return nil, err
	}
	invs, err := s.store.List(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	return invs, nil
}

// Revoke deletes an open invitation.
func (s *Service) Revoke(ctx context.Context, c access.Caller, id ID) error {
	scope, err := s.authorize(c, false)
	if err != nil {
		return err
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "invitation.revoke", Target: "invitation:" + id.String()}
	if err := s.store.Revoke(ctx, scope, id, audit); err != nil {
		return fmt.Errorf("revoke invitation: %w", err)
	}
	return nil
}

// Inspect returns the invitation of a link (for the accept page).
func (s *Service) Inspect(ctx context.Context, token string) (Pending, error) {
	if len(token) != tokenLength {
		return Pending{}, ErrInvalidInvitation
	}
	p, found, err := s.store.Lookup(ctx, sha256.Sum256([]byte(token)), s.now())
	if err != nil {
		return Pending{}, fmt.Errorf("look up invitation: %w", err)
	}
	if !found {
		return Pending{}, ErrInvalidInvitation
	}
	return p, nil
}

// AcceptNewAccount creates an account for the invited address and joins the organization.
func (s *Service) AcceptNewAccount(ctx context.Context, token, displayName, pw string) (Pending, error) {
	p, err := s.Inspect(ctx, token)
	if err != nil {
		return Pending{}, err
	}
	name := strings.TrimSpace(displayName)
	if name == "" || utf8.RuneCountInString(name) > maxName || strings.ContainsFunc(name, unicode.IsControl) {
		return Pending{}, ErrInvalidName
	}
	if err := password.Validate(pw, p.Email, name); err != nil {
		return Pending{}, fmt.Errorf("password: %w", err)
	}
	hash, err := s.hasher.Hash(pw)
	if err != nil {
		return Pending{}, fmt.Errorf("hash password: %w", err)
	}
	account := NewAccount{User: identity.UserID(s.newID()), DisplayName: name, PasswordHash: hash}
	if err := s.store.AcceptNewAccount(ctx, p, account, s.newID(), s.now(), joinAudit(account.User, p)); err != nil {
		return Pending{}, fmt.Errorf("accept invitation: %w", err)
	}
	return p, nil
}

// Join adds a logged-in user to the organization when the invitation is for the user's address.
func (s *Service) Join(ctx context.Context, token string, user identity.UserID) (Pending, error) {
	p, err := s.Inspect(ctx, token)
	if err != nil {
		return Pending{}, err
	}
	profile, found, err := s.profiles.Profile(ctx, user)
	if err != nil {
		return Pending{}, fmt.Errorf("load profile: %w", err)
	}
	if !found || !strings.EqualFold(profile.Email, p.Email) {
		return Pending{}, ErrInvalidInvitation
	}
	if err := s.store.AcceptExisting(ctx, p, user, s.newID(), joinAudit(user, p)); err != nil {
		return Pending{}, fmt.Errorf("accept invitation: %w", err)
	}
	return p, nil
}

// authorize requires an org admin (ManageMembers) and, for invitations, a recent step-up.
func (s *Service) authorize(c access.Caller, stepUp bool) (tenant.Scope, error) {
	scope, err := access.Authorize(s.policy, c, authz.ManageMembers, stepUp, s.now())
	if err != nil {
		return tenant.Scope{}, fmt.Errorf("authorize: %w", err)
	}
	return scope, nil
}

func joinAudit(user identity.UserID, p Pending) access.AuditEntry {
	return access.AuditEntry{
		Actor: user, Action: "member.join", Target: "user:" + user.String(),
		Details: map[string]string{"invitation": p.ID.String(), "role": p.Role.String(), "group": groupName(p.Group)},
	}
}

func groupName(g *tenant.GroupID) string {
	if g == nil {
		return "*"
	}
	return g.String()
}
