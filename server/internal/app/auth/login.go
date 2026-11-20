// Package auth implements the login use case (KONZEPT §7): credential check with account lockout,
// per-IP rate limiting and session creation. All failures look the same to the client.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Lockout policy: from the 5th consecutive failure the account is locked for 1, 2, 4, … minutes,
// at most 15 minutes.
const (
	lockAfterFailures = 5
	firstLock         = time.Minute
	maxLock           = 15 * time.Minute
	maxEmailLength    = 320
)

var (
	// ErrInvalidCredentials covers unknown users, wrong passwords and locked accounts alike.
	ErrInvalidCredentials = errors.New("login failed: wrong e-mail or password, or the account is temporarily locked")
	// ErrWrongPassword means a password confirmation (step-up) failed: wrong password or a
	// temporarily locked account, deliberately not distinguished.
	ErrWrongPassword = errors.New("password confirmation failed: wrong password, or the account is temporarily locked")
	// ErrTooManyAttempts means the client IP exceeded the login rate limit.
	ErrTooManyAttempts = errors.New("too many login attempts, please wait a moment")
)

// Credentials are the password credentials of a user.
type Credentials struct {
	User           identity.UserID
	PasswordHash   string
	FailedAttempts int32
	LockedUntil    *time.Time
}

// Organization is an organization the user belongs to.
type Organization struct {
	ID   tenant.OrgID
	Name string
}

// Profile is the public part of a user identity.
type Profile struct {
	Email       string
	DisplayName string
}

// CredentialRepository persists credentials and answers tenant-less login lookups.
type CredentialRepository interface {
	FindUserByEmail(ctx context.Context, email string) (identity.UserID, bool, error)
	Get(ctx context.Context, user identity.UserID) (Credentials, bool, error)
	RecordFailedLogin(ctx context.Context, user identity.UserID, lockedUntil *time.Time) error
	ResetFailedLogins(ctx context.Context, user identity.UserID) error
	UpdatePasswordHash(ctx context.Context, user identity.UserID, hash string) error
	Organizations(ctx context.Context, user identity.UserID) ([]Organization, error)
	Profile(ctx context.Context, user identity.UserID) (Profile, bool, error)
}

// RateLimiter limits attempts per key (client IP).
type RateLimiter interface {
	Allow(key string) bool
}

// Deps are the dependencies of the Service.
type Deps struct {
	Credentials CredentialRepository
	Sessions    *session.Manager
	Hasher      *password.Hasher
	IPLimiter   RateLimiter
	Now         func() time.Time
}

// Service performs logins.
type Service struct{ d Deps }

// NewService returns a login Service.
func NewService(d Deps) *Service { return &Service{d: d} }

// LoginResult is the outcome of a successful login.
type LoginResult struct {
	Token         string // session cookie value
	Session       session.Session
	Organizations []Organization
}

// Login checks e-mail and password and starts a session in the user's first organization.
func (s *Service) Login(ctx context.Context, email, pw, clientIP string) (LoginResult, error) {
	if !s.d.IPLimiter.Allow(clientIP) {
		return LoginResult{}, ErrTooManyAttempts
	}
	email = strings.TrimSpace(email)
	if email == "" || len(email) > maxEmailLength || pw == "" || len(pw) > password.MaxLength {
		return LoginResult{}, ErrInvalidCredentials
	}

	user, found, err := s.d.Credentials.FindUserByEmail(ctx, email)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	var creds Credentials
	if found {
		creds, found, err = s.d.Credentials.Get(ctx, user)
		if err != nil {
			return LoginResult{}, fmt.Errorf("login: %w", err)
		}
	}
	now := s.d.Now()
	if !found || (creds.LockedUntil != nil && now.Before(*creds.LockedUntil)) {
		// Same cost as a real check, so response times do not reveal existing or locked accounts.
		s.d.Hasher.VerifyDummy(pw)
		return LoginResult{}, ErrInvalidCredentials
	}

	ok, err := s.d.Hasher.Verify(pw, creds.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: verify password: %w", err)
	}
	if !ok {
		if err := s.d.Credentials.RecordFailedLogin(ctx, user, lockUntil(creds.FailedAttempts+1, now)); err != nil {
			return LoginResult{}, fmt.Errorf("login: %w", err)
		}
		return LoginResult{}, ErrInvalidCredentials
	}
	return s.succeed(ctx, creds, pw)
}

// ConfirmPassword re-authenticates the user of a session with the password and renews its step-up
// (KONZEPT §7) for sensitive actions. Failures count towards the same lockout as logins, so a
// hijacked session can't be used to guess the password; the IP rate limit applies as well.
func (s *Service) ConfirmPassword(ctx context.Context, token, pw, clientIP string) (session.Session, error) {
	if !s.d.IPLimiter.Allow(clientIP) {
		return session.Session{}, ErrTooManyAttempts
	}
	current, err := s.d.Sessions.Validate(ctx, token)
	if err != nil {
		return session.Session{}, fmt.Errorf("confirm password: %w", err)
	}
	if pw == "" || len(pw) > password.MaxLength {
		return session.Session{}, ErrWrongPassword
	}
	creds, found, err := s.d.Credentials.Get(ctx, current.User)
	if err != nil {
		return session.Session{}, fmt.Errorf("confirm password: %w", err)
	}
	now := s.d.Now()
	if !found || (creds.LockedUntil != nil && now.Before(*creds.LockedUntil)) {
		s.d.Hasher.VerifyDummy(pw)
		return session.Session{}, ErrWrongPassword
	}
	ok, err := s.d.Hasher.Verify(pw, creds.PasswordHash)
	if err != nil {
		return session.Session{}, fmt.Errorf("confirm password: verify: %w", err)
	}
	if !ok {
		if err := s.d.Credentials.RecordFailedLogin(ctx, current.User, lockUntil(creds.FailedAttempts+1, now)); err != nil {
			return session.Session{}, fmt.Errorf("confirm password: %w", err)
		}
		return session.Session{}, ErrWrongPassword
	}
	if creds.FailedAttempts > 0 || creds.LockedUntil != nil {
		if err := s.d.Credentials.ResetFailedLogins(ctx, current.User); err != nil {
			return session.Session{}, fmt.Errorf("confirm password: %w", err)
		}
	}
	stepped, err := s.d.Sessions.StepUp(ctx, token)
	if err != nil {
		return session.Session{}, fmt.Errorf("confirm password: %w", err)
	}
	return stepped, nil
}

func (s *Service) succeed(ctx context.Context, creds Credentials, pw string) (LoginResult, error) {
	if creds.FailedAttempts > 0 || creds.LockedUntil != nil {
		if err := s.d.Credentials.ResetFailedLogins(ctx, creds.User); err != nil {
			return LoginResult{}, fmt.Errorf("login: %w", err)
		}
	}
	if s.d.Hasher.NeedsRehash(creds.PasswordHash) {
		hash, err := s.d.Hasher.Hash(pw)
		if err != nil {
			return LoginResult{}, fmt.Errorf("login: rehash: %w", err)
		}
		if err := s.d.Credentials.UpdatePasswordHash(ctx, creds.User, hash); err != nil {
			return LoginResult{}, fmt.Errorf("login: %w", err)
		}
	}
	orgs, err := s.d.Credentials.Organizations(ctx, creds.User)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	var active tenant.OrgID
	if len(orgs) > 0 {
		active = orgs[0].ID
	}
	token, sess, err := s.d.Sessions.Create(ctx, creds.User, active)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	return LoginResult{Token: token, Session: sess, Organizations: orgs}, nil
}

// lockUntil returns the lock end for the given number of consecutive failures (nil = no lock).
func lockUntil(failures int32, now time.Time) *time.Time {
	if failures < lockAfterFailures {
		return nil
	}
	d := firstLock
	for i := lockAfterFailures; i < int(failures) && d < maxLock; i++ {
		d *= 2
	}
	d = min(d, maxLock)
	until := now.Add(d)
	return &until
}
