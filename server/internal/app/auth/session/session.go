// Package session manages server-side login sessions (KONZEPT §7): random tokens in a cookie,
// only their SHA-256 hash in the database, idle and absolute timeouts, step-up tracking.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Timeouts (KONZEPT §7).
const (
	IdleTimeout     = 30 * time.Minute
	AbsoluteTimeout = 12 * time.Hour
	// touchInterval limits writes: last activity is stored at most once per interval.
	touchInterval = time.Minute
	tokenBytes    = 32
	// maxTokenLength rejects absurd cookie values before hashing.
	maxTokenLength = 64
)

// ErrInvalid means the session is unknown, expired or revoked (deliberately not distinguished).
var ErrInvalid = errors.New("invalid or expired session")

// Session is a login session. ID is the hex SHA-256 of the token; the token itself is never stored.
type Session struct {
	ID         string
	User       identity.UserID
	Org        tenant.OrgID // active organization; zero for instance-level sessions
	CreatedAt  time.Time
	LastSeenAt time.Time
	StepUpAt   time.Time // last (re-)authentication, e.g. login or 2FA confirmation
}

// SteppedUpWithin reports whether the user authenticated within maxAge (step-up check for
// sensitive actions such as updates, reboots or role changes).
func (s Session) SteppedUpWithin(maxAge time.Duration, now time.Time) bool {
	return now.Sub(s.StepUpAt) <= maxAge
}

// Store persists sessions.
type Store interface {
	Insert(ctx context.Context, s Session) error
	Get(ctx context.Context, id string) (Session, bool, error)
	Touch(ctx context.Context, id string, at time.Time) error
	SetStepUp(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteForUser(ctx context.Context, user identity.UserID) error
}

// Manager creates and validates sessions.
type Manager struct {
	store Store
	now   func() time.Time
}

// NewManager returns a Manager.
func NewManager(store Store, now func() time.Time) *Manager {
	return &Manager{store: store, now: now}
}

// Create starts a new session after a successful login and returns the token for the cookie.
// A new token on every login prevents session fixation.
func (m *Manager) Create(ctx context.Context, user identity.UserID, org tenant.OrgID) (string, Session, error) {
	now := m.now()
	return m.insert(ctx, Session{User: user, Org: org, CreatedAt: now, LastSeenAt: now, StepUpAt: now})
}

// SwitchOrganization moves the session to another organization under a new token. The caller
// must have checked the membership. Creation and step-up time are kept: switching neither
// extends the absolute timeout nor counts as a re-authentication.
func (m *Manager) SwitchOrganization(ctx context.Context, token string, org tenant.OrgID) (string, Session, error) {
	old, err := m.Validate(ctx, token)
	if err != nil {
		return "", Session{}, err
	}
	// Revoke first: on a later failure the user is logged out rather than holding two sessions.
	if err := m.store.Delete(ctx, old.ID); err != nil {
		return "", Session{}, fmt.Errorf("delete previous session: %w", err)
	}
	return m.insert(ctx, Session{User: old.User, Org: org, CreatedAt: old.CreatedAt, LastSeenAt: m.now(), StepUpAt: old.StepUpAt})
}

func (m *Manager) insert(ctx context.Context, s Session) (string, Session, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Session{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.ID = hashToken(token)
	if err := m.store.Insert(ctx, s); err != nil {
		return "", Session{}, fmt.Errorf("store session: %w", err)
	}
	return token, s, nil
}

// Validate returns the session of token, renews its activity and removes it when it expired.
func (m *Manager) Validate(ctx context.Context, token string) (Session, error) {
	s, err := m.lookup(ctx, token)
	if err != nil {
		return Session{}, err
	}
	now := m.now()
	if now.Sub(s.LastSeenAt) >= IdleTimeout || now.Sub(s.CreatedAt) >= AbsoluteTimeout {
		if err := m.store.Delete(ctx, s.ID); err != nil {
			return Session{}, fmt.Errorf("delete expired session: %w", err)
		}
		return Session{}, ErrInvalid
	}
	if now.Sub(s.LastSeenAt) >= touchInterval {
		if err := m.store.Touch(ctx, s.ID, now); err != nil {
			return Session{}, fmt.Errorf("renew session: %w", err)
		}
		s.LastSeenAt = now
	}
	return s, nil
}

// StepUp records a successful re-authentication (password or 2FA confirmation) and returns the
// updated session.
func (m *Manager) StepUp(ctx context.Context, token string) (Session, error) {
	s, err := m.Validate(ctx, token)
	if err != nil {
		return Session{}, err
	}
	now := m.now()
	if err := m.store.SetStepUp(ctx, s.ID, now); err != nil {
		return Session{}, fmt.Errorf("record step-up: %w", err)
	}
	s.StepUpAt = now
	return s, nil
}

// Revoke ends a session (logout).
func (m *Manager) Revoke(ctx context.Context, token string) error {
	if err := m.store.Delete(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// RevokeAllForUser ends all sessions of a user (password change, account lock).
func (m *Manager) RevokeAllForUser(ctx context.Context, user identity.UserID) error {
	if err := m.store.DeleteForUser(ctx, user); err != nil {
		return fmt.Errorf("delete sessions of user: %w", err)
	}
	return nil
}

func (m *Manager) lookup(ctx context.Context, token string) (Session, error) {
	if token == "" || len(token) > maxTokenLength {
		return Session{}, ErrInvalid
	}
	s, found, err := m.store.Get(ctx, hashToken(token))
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	if !found {
		return Session{}, ErrInvalid
	}
	return s, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
