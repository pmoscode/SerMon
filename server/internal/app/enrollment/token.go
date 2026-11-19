// Package enrollment issues and redeems enrollment tokens for new agents (KONZEPT §6, M1-08).
// A token is "sme_" + 256 random bits (base64url); only the SHA-256 of the whole token is
// stored. It is bound to an organization and a node group, expires (default 1 h) and allows a
// limited number of uses (default 1, up to 1000 for mass rollouts).
package enrollment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Limits (KONZEPT §6).
const (
	DefaultTTL = time.Hour
	MinTTL     = 5 * time.Minute
	MaxTTL     = 7 * 24 * time.Hour
	MaxUses    = 1000

	prefix      = "sme_"
	secretBytes = 32
	tokenLength = len(prefix) + 43
)

var (
	// ErrInvalidToken means the token is malformed, unknown, expired, revoked or used up
	// (deliberately not distinguished).
	ErrInvalidToken = errors.New("invalid or expired enrollment token")
	// ErrInvalidLimits means lifetime or number of uses are out of range.
	ErrInvalidLimits = fmt.Errorf("lifetime must be %v to %v and uses 1 to %d", MinTTL, MaxTTL, MaxUses)
)

// TokenID identifies an enrollment token.
type TokenID uuid.UUID

// String returns the UUID.
func (id TokenID) String() string { return uuid.UUID(id).String() }

// Token is an issued enrollment token (without the secret).
type Token struct {
	ID        TokenID
	Group     tenant.GroupID
	CreatedBy identity.UserID
	CreatedAt time.Time
	ExpiresAt time.Time
	MaxUses   int
	Uses      int
}

// Stored is a valid token found by its hash (redemption, no tenant known yet).
type Stored struct {
	ID    TokenID
	Org   tenant.OrgID
	Group tenant.GroupID
	Hash  [32]byte
}

// Grant is what a redeemed token allows: a new node in this organization and group.
type Grant struct {
	Token TokenID
	Org   tenant.OrgID
	Group tenant.GroupID
}

// Store persists enrollment tokens. Create, Revoke and Consume write the audit entry in the same
// transaction. Revoke and List only see tokens of the scope's groups.
type Store interface {
	Create(ctx context.Context, scope tenant.Scope, t Token, hash [32]byte, audit access.AuditEntry) error
	List(ctx context.Context, scope tenant.Scope) ([]Token, error)
	Revoke(ctx context.Context, scope tenant.Scope, id TokenID, audit access.AuditEntry) error
	// Lookup finds a token that is unexpired, not revoked and not used up at now.
	Lookup(ctx context.Context, hash [32]byte, now time.Time) (Stored, bool, error)
	// Consume uses the token once; ErrInvalidToken if it became invalid in the meantime.
	Consume(ctx context.Context, s Stored, now time.Time, audit access.AuditEntry) error
}

// Service manages enrollment tokens.
type Service struct {
	store  Store
	policy authz.Policy
	now    func() time.Time
	newID  func() uuid.UUID
}

// NewService returns a Service.
func NewService(store Store, policy authz.Policy, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, policy: policy, now: now, newID: newID}
}

// Create issues a token for the group (operator or admin on it). ttl and maxUses of 0 select the
// defaults. The secret is returned once and never stored.
func (s *Service) Create(ctx context.Context, c access.Caller, group tenant.GroupID, ttl time.Duration, maxUses int) (string, Token, error) {
	p := c.Principal
	if !s.policy.Allowed(p, authz.CreateEnrollmentTokens, authz.InGroup(p.Org, group)) {
		return "", Token{}, access.ErrForbidden
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if maxUses == 0 {
		maxUses = 1
	}
	if ttl < MinTTL || ttl > MaxTTL || maxUses < 1 || maxUses > MaxUses {
		return "", Token{}, ErrInvalidLimits
	}
	scope, err := tenant.ForGroups(p.Org, group)
	if err != nil {
		return "", Token{}, access.ErrForbidden
	}
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, fmt.Errorf("generate enrollment token: %w", err)
	}
	secret := prefix + base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	t := Token{ID: TokenID(s.newID()), Group: group, CreatedBy: p.User, CreatedAt: now, ExpiresAt: now.Add(ttl), MaxUses: maxUses}
	audit := access.AuditEntry{
		Actor: p.User, Action: "enrollment_token.create", Target: "enrollment_token:" + t.ID.String(),
		Details: map[string]string{"group": group.String(), "max_uses": strconv.Itoa(maxUses), "expires": t.ExpiresAt.UTC().Format(time.RFC3339)},
	}
	if err := s.store.Create(ctx, scope, t, sha256.Sum256([]byte(secret)), audit); err != nil {
		return "", Token{}, fmt.Errorf("store enrollment token: %w", err)
	}
	return secret, t, nil
}

// List returns the tokens of all groups where the caller is at least operator.
func (s *Service) List(ctx context.Context, c access.Caller) ([]Token, error) {
	scope, err := authz.ScopeFor(c.Principal, role.Operator)
	if err != nil {
		return nil, access.ErrForbidden
	}
	tokens, err := s.store.List(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list enrollment tokens: %w", err)
	}
	return tokens, nil
}

// Revoke makes a token unusable (only in groups where the caller is at least operator).
func (s *Service) Revoke(ctx context.Context, c access.Caller, id TokenID) error {
	scope, err := authz.ScopeFor(c.Principal, role.Operator)
	if err != nil {
		return access.ErrForbidden
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "enrollment_token.revoke", Target: "enrollment_token:" + id.String()}
	if err := s.store.Revoke(ctx, scope, id, audit); err != nil {
		return fmt.Errorf("revoke enrollment token: %w", err)
	}
	return nil
}

// Redeem checks a token presented by an enrolling agent and uses it once.
func (s *Service) Redeem(ctx context.Context, secret string) (Grant, error) {
	if len(secret) != tokenLength || !strings.HasPrefix(secret, prefix) {
		return Grant{}, ErrInvalidToken
	}
	hash := sha256.Sum256([]byte(secret))
	stored, found, err := s.store.Lookup(ctx, hash, s.now())
	if err != nil {
		return Grant{}, fmt.Errorf("look up enrollment token: %w", err)
	}
	// The lookup is by hash; the constant-time comparison is defense in depth.
	if !found || subtle.ConstantTimeCompare(stored.Hash[:], hash[:]) != 1 {
		return Grant{}, ErrInvalidToken
	}
	audit := access.AuditEntry{
		Action: "enrollment_token.use", Target: "enrollment_token:" + stored.ID.String(),
		Details: map[string]string{"group": stored.Group.String()},
	}
	if err := s.store.Consume(ctx, stored, s.now(), audit); err != nil {
		return Grant{}, fmt.Errorf("use enrollment token: %w", err)
	}
	return Grant{Token: stored.ID, Org: stored.Org, Group: stored.Group}, nil
}
