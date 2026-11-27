// Package invitation lets org admins invite members by a single-use link (ADR 0008). The link
// token has 256 bits, only its SHA-256 is stored, it is valid for 72 hours. Creating and listing
// invitations never reveals whether an e-mail address already has an account; whoever holds the
// link learns it when accepting (accepted residual risk, ADR 0008).
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

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
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
	// ErrMailFailed means the invitation was stored but its mail could not be sent; inviting the
	// address again replaces it and sends a new link.
	ErrMailFailed = errors.New("the invitation mail could not be sent")
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

// Mailer sends plain text mail (adapters/smtp).
type Mailer interface {
	SendText(ctx context.Context, to, subject, text string) error
}

// Service manages invitations.
type Service struct {
	store    Store
	profiles Profiles
	policy   authz.Policy
	hasher   Hasher
	now      func() time.Time
	newID    func() uuid.UUID
	mailer   Mailer
	baseURL  string
}

// Option configures a Service.
type Option func(*Service)

// WithMailer sends invitation links by mail instead of returning them (production, M1-39): only
// the owner of the mailbox can accept, which proves the address. baseURL is the origin of the UI.
func WithMailer(mailer Mailer, baseURL string) Option {
	return func(s *Service) { s.mailer, s.baseURL = mailer, strings.TrimRight(baseURL, "/") }
}

// NewService returns a Service.
func NewService(store Store, profiles Profiles, policy authz.Policy, hasher Hasher, now func() time.Time, newID func() uuid.UUID, options ...Option) *Service {
	s := &Service{store: store, profiles: profiles, policy: policy, hasher: hasher, now: now, newID: newID}
	for _, option := range options {
		option(s)
	}
	return s
}

// Invite creates an invitation (replacing an open one for the same address) and returns the
// link token – or "" if the link went out by mail (WithMailer); then nobody but the mailbox owner
// ever sees it. Inviting grants a role, so it needs a step-up like other changes of rights.
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
	token, hash, err := NewToken()
	if err != nil {
		return "", Invitation{}, err
	}
	now := s.now()
	inviter := c.Principal.User
	inv := Invitation{ID: ID(s.newID()), Email: email, Group: group, Role: r, InvitedBy: &inviter, CreatedAt: now, ExpiresAt: now.Add(Lifetime)}
	// No address in the log: it stays as long as the log, the invitation only until it expires
	// (V1-17); the target names the invitation, its join entry the account.
	audit := access.AuditEntry{
		Actor: inviter, Action: "member.invite", Target: "invitation:" + inv.ID.String(),
		Details: map[string]string{"role": r.String(), "group": groupName(group)},
	}
	if err := s.store.Create(ctx, scope, inv, hash, audit); err != nil {
		return "", Invitation{}, fmt.Errorf("create invitation: %w", err)
	}
	if s.mailer == nil {
		return token, inv, nil
	}
	orgName := ""
	if pending, ok, err := s.store.Lookup(ctx, hash, now); err == nil && ok {
		orgName = pending.OrgName
	}
	inviterName := ""
	if p, ok, err := s.profiles.Profile(ctx, inviter); err == nil && ok {
		inviterName = p.DisplayName
	}
	if err := SendMail(ctx, s.mailer, s.baseURL, token, MailFacts{
		To: email, Organization: orgName, InvitedBy: inviterName, Role: r, ExpiresAt: inv.ExpiresAt,
	}); err != nil {
		return "", inv, err
	}
	return "", inv, nil
}

// MailFacts are the details an invitation mail names.
type MailFacts struct {
	To, Organization, InvitedBy string
	Role                        role.Role
	ExpiresAt                   time.Time
}

// SendMail sends the invitation link <baseURL>/invite#<token> to f.To, in German and English
// (the language of the invited person is unknown).
func SendMail(ctx context.Context, mailer Mailer, baseURL, token string, f MailFacts) error {
	link := strings.TrimRight(baseURL, "/") + "/invite#" + token
	org := f.Organization
	if org == "" {
		org = "SerMon"
	}
	de := "Du wurdest zur Organisation „" + org + "“ in SerMon eingeladen"
	en := "You have been invited to the organization \"" + org + "\" in SerMon"
	if f.InvitedBy != "" {
		de = f.InvitedBy + " hat dich zur Organisation „" + org + "“ in SerMon eingeladen"
		en = f.InvitedBy + " invited you to the organization \"" + org + "\" in SerMon"
	}
	until := f.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	subject := "SerMon: Einladung zu „" + org + "“ / Invitation to \"" + org + "\""
	text := de + " (Rolle: " + f.Role.String() + ").\nAnnehmen bis " + until + ":\n" + link + "\n\n" +
		en + " (role: " + f.Role.String() + ").\nAccept until " + until + ":\n" + link + "\n\n" +
		"Wenn du diese Einladung nicht erwartest, ignoriere diese Nachricht.\n" +
		"If you don't expect this invitation, ignore this message.\n"
	if err := mailer.SendText(ctx, f.To, subject, text); err != nil {
		return fmt.Errorf("%w: %w", ErrMailFailed, err)
	}
	return nil
}

// NewToken returns a new link token (256 bits, base64url) and the SHA-256 that is stored.
func NewToken() (string, [32]byte, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate invitation token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, sha256.Sum256([]byte(token)), nil
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
	name, ok := naming.Clean(displayName, maxName)
	if !ok {
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
