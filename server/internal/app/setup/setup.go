// Package setup completes the first installation (KONZEPT §16, M1-05): a one-time setup token
// (30 min, only its hash is stored) creates the first superadmin with the first organization.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// TokenLifetime is how long a setup token is valid.
const TokenLifetime = 30 * time.Minute

const (
	tokenBytes  = 32
	tokenLength = 43 // base64url without padding of 32 bytes
	maxName     = 200
)

var (
	// ErrAlreadyInitialized means the setup was completed before.
	ErrAlreadyInitialized = errors.New("instance is already initialized")
	// ErrInvalidToken means the setup token is unknown, expired or used (not distinguished).
	ErrInvalidToken = errors.New("invalid or expired setup token")
	// ErrInvalidEmail means the e-mail address is malformed.
	ErrInvalidEmail = errors.New("invalid e-mail address")
	// ErrInvalidName means a name is empty, too long or contains control characters.
	ErrInvalidName = fmt.Errorf("names must have 1 to %d characters and no control characters", maxName)
)

// FirstAdmin is the superadmin and organization the setup creates.
type FirstAdmin struct {
	User             identity.UserID
	Email            string
	DisplayName      string
	PasswordHash     string
	Org              tenant.OrgID
	OrganizationName string
	Binding          uuid.UUID // org-wide admin binding of the user in the organization
}

// Store persists the setup state.
type Store interface {
	Completed(ctx context.Context) (bool, error)
	// IssueToken replaces any previous token; ErrAlreadyInitialized after the setup.
	IssueToken(ctx context.Context, hash [32]byte, expires time.Time) error
	// Complete claims the token (ErrInvalidToken unless valid at now) and creates the admin in
	// the same transaction.
	Complete(ctx context.Context, hash [32]byte, now time.Time, admin FirstAdmin) error
}

// Hasher hashes passwords (password.Hasher).
type Hasher interface {
	Hash(pw string) (string, error)
}

// Request is the input of the setup wizard.
type Request struct {
	Token            string
	Email            string
	DisplayName      string
	Password         string
	OrganizationName string
}

// Service runs the first installation.
type Service struct {
	store  Store
	hasher Hasher
	now    func() time.Time
	newID  func() uuid.UUID
}

// NewService returns a Service.
func NewService(store Store, hasher Hasher, now func() time.Time, newID func() uuid.UUID) *Service {
	return &Service{store: store, hasher: hasher, now: now, newID: newID}
}

// Required reports whether the setup is still open.
func (s *Service) Required(ctx context.Context) (bool, error) {
	completed, err := s.store.Completed(ctx)
	if err != nil {
		return false, fmt.Errorf("load setup state: %w", err)
	}
	return !completed, nil
}

// IssueToken creates a new setup token (invalidating the previous one) for the operator.
func (s *Service) IssueToken(ctx context.Context) (string, time.Time, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate setup token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := s.now().Add(TokenLifetime)
	if err := s.store.IssueToken(ctx, sha256.Sum256([]byte(token)), expires); err != nil {
		return "", time.Time{}, fmt.Errorf("store setup token: %w", err)
	}
	return token, expires, nil
}

// Complete creates the first superadmin with its organization and consumes the token.
func (s *Service) Complete(ctx context.Context, r Request) error {
	if len(r.Token) != tokenLength {
		return ErrInvalidToken
	}
	email, ok := identity.NormalizeEmail(r.Email)
	if !ok {
		return ErrInvalidEmail
	}
	name, okName := naming.Clean(r.DisplayName, maxName)
	org, okOrg := naming.Clean(r.OrganizationName, maxName)
	if !okName || !okOrg {
		return ErrInvalidName
	}
	if err := password.Validate(r.Password, email, name); err != nil {
		return fmt.Errorf("password: %w", err)
	}
	hash, err := s.hasher.Hash(r.Password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	admin := FirstAdmin{
		User: identity.UserID(s.newID()), Email: email, DisplayName: name, PasswordHash: hash,
		Org: tenant.OrgID(s.newID()), OrganizationName: org, Binding: s.newID(),
	}
	if err := s.store.Complete(ctx, sha256.Sum256([]byte(r.Token)), s.now(), admin); err != nil {
		return fmt.Errorf("complete setup: %w", err)
	}
	return nil
}
