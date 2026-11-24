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
	maxCodeLength     = 64
)

var (
	// ErrInvalidCredentials covers unknown users, wrong passwords and locked accounts alike.
	ErrInvalidCredentials = errors.New("login failed: wrong e-mail or password, or the account is temporarily locked")
	// ErrWrongPassword means a password confirmation (step-up) failed: wrong password or a
	// temporarily locked account, deliberately not distinguished.
	ErrWrongPassword = errors.New("password confirmation failed: wrong password, or the account is temporarily locked")
	// ErrWrongCode means a second-factor code failed: wrong, already used or the account is
	// temporarily locked, deliberately not distinguished.
	ErrWrongCode = errors.New("the code is wrong or was already used, or the account is temporarily locked")
	// ErrSecondFactorRequired means users with two-factor authentication confirm with the second
	// factor, not with the password.
	ErrSecondFactorRequired = errors.New("confirm with your second factor")
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
	// ReserveAttempt counts an attempt before it is checked, atomically: only if the counter is
	// still seen and the account isn't locked (compare-and-swap); lockedUntil applies from now on.
	// Parallel requests thus can't all read the same unlocked state and get a full check each.
	ReserveAttempt(ctx context.Context, user identity.UserID, seen int32, lockedUntil *time.Time, now time.Time) (bool, error)
	// ReleaseAttempt takes a reserved attempt back (a right password that still needs the second
	// factor); a lock ends if fewer than lockAfter attempts remain.
	ReleaseAttempt(ctx context.Context, user identity.UserID, lockAfter int32) error
	ResetFailedLogins(ctx context.Context, user identity.UserID) error
	UpdatePasswordHash(ctx context.Context, user identity.UserID, hash string) error
	Organizations(ctx context.Context, user identity.UserID) ([]Organization, error)
	Profile(ctx context.Context, user identity.UserID) (Profile, bool, error)
}

// SecondFactor checks second factors (twofactor.Service).
type SecondFactor interface {
	Enabled(ctx context.Context, user identity.UserID) (bool, error)
	// Verify checks a TOTP or recovery code; every code works once.
	Verify(ctx context.Context, user identity.UserID, code string) (bool, error)
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
	// SecondFactor is optional; without it nobody has a second factor.
	SecondFactor SecondFactor
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
	// SecondFactorRequired means the session waits for VerifySecondFactor (no organizations yet).
	SecondFactorRequired bool
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
	reserved := false
	if found {
		creds, reserved, err = s.attempt(ctx, user)
		if err != nil {
			return LoginResult{}, fmt.Errorf("login: %w", err)
		}
	}
	if !reserved {
		// Same cost as a real check, so response times do not reveal existing or locked accounts.
		s.d.Hasher.VerifyDummy(pw)
		return LoginResult{}, ErrInvalidCredentials
	}

	ok, err := s.d.Hasher.Verify(pw, creds.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: verify password: %w", err)
	}
	if !ok {
		return LoginResult{}, ErrInvalidCredentials // the attempt is already counted
	}
	return s.succeed(ctx, creds, pw)
}

// ConfirmPassword re-authenticates the user of a session with the password and renews its step-up
// (KONZEPT §7) for sensitive actions. Failures count towards the same lockout as logins, so a
// hijacked session can't be used to guess the password; the IP rate limit applies as well. Users
// with a second factor confirm with it instead (ErrSecondFactorRequired).
func (s *Service) ConfirmPassword(ctx context.Context, token, pw, clientIP string) (session.Session, error) {
	if !s.d.IPLimiter.Allow(clientIP) {
		return session.Session{}, ErrTooManyAttempts
	}
	current, err := s.d.Sessions.Validate(ctx, token)
	if err != nil {
		return session.Session{}, fmt.Errorf("confirm password: %w", err)
	}
	if enabled, err := s.secondFactorEnabled(ctx, current.User); err != nil || enabled {
		if err != nil {
			return session.Session{}, err
		}
		return session.Session{}, ErrSecondFactorRequired
	}
	if pw == "" || len(pw) > password.MaxLength {
		return session.Session{}, ErrWrongPassword
	}
	err = s.checkFactor(ctx, current.User, pw, ErrWrongPassword, func(creds Credentials) (bool, error) {
		ok, err := s.d.Hasher.Verify(pw, creds.PasswordHash)
		if err != nil {
			return false, fmt.Errorf("verify password: %w", err)
		}
		return ok, nil
	})
	if err != nil {
		return session.Session{}, fmt.Errorf("confirm password: %w", err)
	}
	return s.stepUp(ctx, token)
}

// VerifySecondFactor completes a login that waits for the second factor and returns the full
// session under a new token. Failures count towards the lockout.
func (s *Service) VerifySecondFactor(ctx context.Context, token, code, clientIP string) (LoginResult, error) {
	if !s.d.IPLimiter.Allow(clientIP) {
		return LoginResult{}, ErrTooManyAttempts
	}
	pending, err := s.d.Sessions.ValidatePending(ctx, token)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify second factor: %w", err)
	}
	if err := s.checkSecondFactor(ctx, pending.User, code); err != nil {
		return LoginResult{}, fmt.Errorf("verify second factor: %w", err)
	}
	full, sess, err := s.d.Sessions.CompleteSecondFactor(ctx, token)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify second factor: %w", err)
	}
	orgs, err := s.d.Credentials.Organizations(ctx, sess.User)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify second factor: %w", err)
	}
	return LoginResult{Token: full, Session: sess, Organizations: orgs}, nil
}

// ConfirmSecondFactor renews the step-up of a full session with a TOTP or recovery code.
func (s *Service) ConfirmSecondFactor(ctx context.Context, token, code, clientIP string) (session.Session, error) {
	if !s.d.IPLimiter.Allow(clientIP) {
		return session.Session{}, ErrTooManyAttempts
	}
	current, err := s.d.Sessions.Validate(ctx, token)
	if err != nil {
		return session.Session{}, fmt.Errorf("confirm second factor: %w", err)
	}
	if err := s.checkSecondFactor(ctx, current.User, code); err != nil {
		return session.Session{}, fmt.Errorf("confirm second factor: %w", err)
	}
	return s.stepUp(ctx, token)
}

func (s *Service) checkSecondFactor(ctx context.Context, user identity.UserID, code string) error {
	if s.d.SecondFactor == nil || code == "" || len(code) > maxCodeLength {
		return ErrWrongCode
	}
	return s.checkFactor(ctx, user, "", ErrWrongCode, func(Credentials) (bool, error) {
		ok, err := s.d.SecondFactor.Verify(ctx, user, code)
		if err != nil {
			return false, fmt.Errorf("verify code: %w", err)
		}
		return ok, nil
	})
}

// VerifyFactor runs a check of another factor (a passkey, M3-23) under the lockout of the
// account: failures count, a locked account fails without a check; ErrWrongCode then. Accounts
// without a password (created over OIDC) have no lockout state: only the IP rate limit applies.
func (s *Service) VerifyFactor(ctx context.Context, user identity.UserID, check func() (bool, error)) error {
	if _, found, err := s.d.Credentials.Get(ctx, user); err != nil {
		return fmt.Errorf("verify factor: %w", err)
	} else if !found {
		ok, err := check()
		if err != nil {
			return err
		}
		if !ok {
			return ErrWrongCode
		}
		return nil
	}
	return s.checkFactor(ctx, user, "", ErrWrongCode, func(Credentials) (bool, error) { return check() })
}

// checkFactor runs check under the lockout of the user's account: locked accounts fail without a
// check (dummy hash for the password), failures count, success resets the counter.
func (s *Service) checkFactor(ctx context.Context, user identity.UserID, pw string, wrong error, check func(Credentials) (bool, error)) error {
	creds, reserved, err := s.attempt(ctx, user)
	if err != nil {
		return err
	}
	if !reserved {
		if pw != "" {
			s.d.Hasher.VerifyDummy(pw)
		}
		return wrong
	}
	ok, err := check(creds)
	if err != nil {
		return err
	}
	if !ok {
		return wrong // the attempt is already counted
	}
	if err := s.d.Credentials.ResetFailedLogins(ctx, user); err != nil {
		return err //nolint:wrapcheck // wrapped by the callers
	}
	return nil
}

// maxReserveTries bounds the compare-and-swap loop of attempt; a busy account fails closed.
const maxReserveTries = 8

// attempt reserves one attempt on the account before it is checked. false: unknown user, locked
// account, or too much contention – all answered like a wrong password.
func (s *Service) attempt(ctx context.Context, user identity.UserID) (Credentials, bool, error) {
	for range maxReserveTries {
		creds, found, err := s.d.Credentials.Get(ctx, user)
		if err != nil || !found {
			return Credentials{}, false, err //nolint:wrapcheck // wrapped by the callers
		}
		now := s.d.Now()
		if creds.LockedUntil != nil && now.Before(*creds.LockedUntil) {
			return Credentials{}, false, nil
		}
		ok, err := s.d.Credentials.ReserveAttempt(ctx, user, creds.FailedAttempts, lockUntil(creds.FailedAttempts+1, now), now)
		if err != nil {
			return Credentials{}, false, err //nolint:wrapcheck // wrapped by the callers
		}
		if ok {
			creds.FailedAttempts++
			return creds, true, nil
		}
	}
	return Credentials{}, false, nil
}

func (s *Service) stepUp(ctx context.Context, token string) (session.Session, error) {
	stepped, err := s.d.Sessions.StepUp(ctx, token)
	if err != nil {
		return session.Session{}, fmt.Errorf("step-up: %w", err)
	}
	return stepped, nil
}

func (s *Service) secondFactorEnabled(ctx context.Context, user identity.UserID) (bool, error) {
	if s.d.SecondFactor == nil {
		return false, nil
	}
	enabled, err := s.d.SecondFactor.Enabled(ctx, user)
	if err != nil {
		return false, fmt.Errorf("second factor: %w", err)
	}
	return enabled, nil
}

func (s *Service) succeed(ctx context.Context, creds Credentials, pw string) (LoginResult, error) {
	enabled, err := s.secondFactorEnabled(ctx, creds.User)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	// With a second factor the failures are reset only after it: otherwise someone who knows the
	// password could alternate it with code guesses and never reach the lockout. The right password
	// only takes its own attempt back.
	if enabled {
		err = s.d.Credentials.ReleaseAttempt(ctx, creds.User, lockAfterFailures)
	} else {
		err = s.d.Credentials.ResetFailedLogins(ctx, creds.User)
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
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
	if enabled {
		token, sess, err := s.d.Sessions.CreatePending(ctx, creds.User, active)
		if err != nil {
			return LoginResult{}, fmt.Errorf("login: %w", err)
		}
		return LoginResult{Token: token, Session: sess, SecondFactorRequired: true}, nil
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
