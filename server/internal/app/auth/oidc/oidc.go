// Package oidc implements the login over an OpenID Connect provider (M3-20, ADR 0033): the
// authorization code flow with PKCE (S256), state and nonce bound to the browser that started it,
// identities by issuer and subject, linking an identity to the signed-in user and the step-up
// by a fresh login at the provider.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Intent is what a flow is for.
type Intent int

// Intents: log in, link an identity to the signed-in user, renew the step-up.
const (
	IntentLogin Intent = iota + 1
	IntentLink
	IntentStepUp
)

// Bounds of a flow.
const (
	FlowLifetime = 10 * time.Minute
	// authTimeSkew tolerates clocks of the provider behind ours.
	authTimeSkew = time.Minute
	maxNext      = 512
	defaultNext  = "/"
)

var (
	// ErrFailed covers every failed login, link or step-up alike: the browser learns nothing about
	// accounts or the reason (logged on the server).
	ErrFailed = errors.New("the sign-in with the identity provider failed")
	// ErrTooManyAttempts means the client IP exceeded the rate limit.
	ErrTooManyAttempts = errors.New("too many attempts, please wait a moment")
	// ErrNotSignedIn means linking and step-up need a session.
	ErrNotSignedIn = errors.New("sign in first")
	// ErrUnknownIdentity means no user has this identity (and no provisioning applies).
	ErrUnknownIdentity = errors.New("no account has this identity")
	// ErrIdentityTaken means the identity belongs to another user, or the user has one of the
	// provider already.
	ErrIdentityTaken = errors.New("the identity is linked already")
	// ErrLastSignIn means unlinking would leave the user no way to sign in.
	ErrLastSignIn = errors.New("set a password first: this is your only way to sign in")
	// ErrNotLinked means the user has no identity of the issuer.
	ErrNotLinked = errors.New("no identity of this provider is linked")
)

// Claims are the verified claims of an ID token.
type Claims struct {
	Issuer, Subject string
	Email           string
	EmailVerified   bool
	Name            string
	Groups          []string
	AMR             []string
	AuthTime        time.Time
	Nonce           string
}

// Provider is the identity provider (adapters/oidc).
type Provider interface {
	// AuthCodeURL returns where the browser logs in; forceLogin asks for a fresh login (step-up).
	AuthCodeURL(ctx context.Context, state, nonce, challenge string, forceLogin bool) (string, error)
	// Exchange redeems the code with the PKCE verifier and verifies the ID token: signature,
	// issuer, audience and expiry.
	Exchange(ctx context.Context, code, verifier string) (Claims, error)
}

// Flow is a started login waiting for the provider.
type Flow struct {
	StateHash   string
	BindingHash string
	Nonce       string
	Verifier    string
	Intent      Intent
	User        identity.UserID
	SessionID   string
	Next        string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// FlowStore keeps the flows (instance-level, every server instance sees them).
type FlowStore interface {
	Insert(ctx context.Context, f Flow) error
	// Take returns and deletes the flow of a state if it has not expired at now.
	Take(ctx context.Context, stateHash string, now time.Time) (Flow, bool, error)
	DeleteExpired(ctx context.Context, now time.Time) error
}

// Identity is an account at the provider linked to a user.
type Identity struct {
	Issuer, Subject string
	User            identity.UserID
	Email           string
}

// IdentityStore keeps the identities.
type IdentityStore interface {
	Find(ctx context.Context, issuer, subject string) (identity.UserID, bool, error)
	// Link stores a new identity of a user with an entry in the instance audit log; ErrIdentityTaken
	// if it belongs to someone or the user has one of the issuer.
	Link(ctx context.Context, id Identity) error
	// Seen records a login with the identity.
	Seen(ctx context.Context, issuer, subject, email string, at time.Time) error
	// OfUser lists the identities of a user (M3-22).
	OfUser(ctx context.Context, user identity.UserID) ([]LinkedIdentity, error)
	// Unlink removes the user's identity of the issuer with an entry in the instance audit log;
	// ErrNotLinked without one.
	Unlink(ctx context.Context, user identity.UserID, issuer string) error
	// HasPassword tells whether the user can sign in with a password.
	HasPassword(ctx context.Context, user identity.UserID) (bool, error)
}

// LinkedIdentity is an identity as the user sees it.
type LinkedIdentity struct {
	Issuer, Subject, Email string
	CreatedAt              time.Time
	LastLoginAt            time.Time // zero: never used to sign in
}

// Users answers what a login needs to know of a user.
type Users interface {
	Organizations(ctx context.Context, user identity.UserID) ([]auth.Organization, error)
}

// SecondFactor tells whether a user has a local second factor (twofactor.Service).
type SecondFactor interface {
	Enabled(ctx context.Context, user identity.UserID) (bool, error)
}

// MFAMode says when a login at the provider counts as several factors.
type MFAMode int

// Modes: by the amr claim, always, never.
const (
	MFAByClaim MFAMode = iota
	MFAAlways
	MFANever
)

// Provisioner keeps accounts and roles in step with the provider's groups (M3-21, oidcmapping).
type Provisioner interface {
	// Reconcile sets and removes the roles the mappings give; join lets the user join
	// organizations whose mappings match (just-in-time provisioning).
	Reconcile(ctx context.Context, user identity.UserID, groups []string, join bool) error
	// Provision creates an account for an identity nobody has, if a mapping matches; false if
	// none is created.
	Provision(ctx context.Context, c Claims) (identity.UserID, bool, error)
}

// RateLimiter limits attempts per client IP.
type RateLimiter interface {
	Allow(key string) bool
}

// Deps are the collaborators of the Service.
type Deps struct {
	Provider     Provider
	Flows        FlowStore
	Identities   IdentityStore
	Users        Users
	Sessions     *session.Manager
	SecondFactor SecondFactor
	Limiter      RateLimiter
	MFA          MFAMode
	// Provisioner reconciles the roles at every sign-in; nil: no group mappings.
	Provisioner Provisioner
	// JIT creates accounts and memberships by the mappings (just-in-time provisioning).
	JIT    bool
	Now    func() time.Time
	Logger *slog.Logger
}

// Service runs the flows.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}
}

// Begun is a started flow: the browser goes to URL and keeps Binding in a cookie.
type Begun struct {
	URL     string
	Binding string
}

// Result is a completed flow. A login has Token (a full session, or Pending one that waits for
// the local second factor); a step-up renewed the session of the cookie.
type Result struct {
	Intent  Intent
	Token   string
	Session session.Session
	Pending bool
	Next    string
}

func random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// challenge is the S256 code challenge of a verifier (RFC 7636).
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SafeNext returns a path inside the app to go to after the flow, "/" for anything else: no
// scheme, host, backslash, dot segment or control character (no open redirect).
func SafeNext(raw string) string {
	if raw == "" || len(raw) > maxNext || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.ContainsAny(raw, "\\") || strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return defaultNext
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" {
		return defaultNext
	}
	p := u.Path
	if p == "" || strings.HasPrefix(p, "//") {
		return defaultNext
	}
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") && clean != "/" {
		clean += "/"
	}
	if clean != p {
		return defaultNext
	}
	return raw
}

// Begin starts a flow. Linking and step-up need the session token; linking also a recent step-up
// (access.ErrStepUpRequired).
func (s *Service) Begin(ctx context.Context, intent Intent, sessionToken, next, clientIP string) (Begun, error) {
	if !s.d.Limiter.Allow(clientIP) {
		return Begun{}, ErrTooManyAttempts
	}
	now := s.d.Now()
	f := Flow{Intent: intent, Next: SafeNext(next), CreatedAt: now, ExpiresAt: now.Add(FlowLifetime)}
	switch intent {
	case IntentLogin:
	case IntentLink, IntentStepUp:
		current, err := s.d.Sessions.Validate(ctx, sessionToken)
		if err != nil {
			return Begun{}, ErrNotSignedIn
		}
		if intent == IntentLink && !current.SteppedUpWithin(access.StepUpMaxAge, now) {
			return Begun{}, access.ErrStepUpRequired
		}
		f.User, f.SessionID = current.User, current.ID
	default:
		return Begun{}, fmt.Errorf("%w: unknown intent", ErrFailed)
	}
	state, err := random(32)
	if err != nil {
		return Begun{}, err
	}
	binding, err := random(32)
	if err != nil {
		return Begun{}, err
	}
	if f.Nonce, err = random(32); err != nil {
		return Begun{}, err
	}
	if f.Verifier, err = random(48); err != nil {
		return Begun{}, err
	}
	f.StateHash, f.BindingHash = hash(state), hash(binding)
	authURL, err := s.d.Provider.AuthCodeURL(ctx, state, f.Nonce, challenge(f.Verifier), intent == IntentStepUp)
	if err != nil {
		return Begun{}, fmt.Errorf("oidc begin: %w", err)
	}
	if err := s.d.Flows.DeleteExpired(ctx, now); err != nil {
		return Begun{}, fmt.Errorf("oidc begin: %w", err)
	}
	if err := s.d.Flows.Insert(ctx, f); err != nil {
		return Begun{}, fmt.Errorf("oidc begin: %w", err)
	}
	return Begun{URL: authURL, Binding: binding}, nil
}

// mfaClaims are the amr values (RFC 8176) that name a second factor.
var mfaClaims = []string{"mfa", "mca", "otp", "hwk", "sc"}

func (s *Service) mfa(c Claims) bool {
	switch s.d.MFA {
	case MFAAlways:
		return true
	case MFANever:
		return false
	case MFAByClaim:
	}
	return slices.ContainsFunc(c.AMR, func(v string) bool { return slices.Contains(mfaClaims, v) })
}

// fail logs the reason and answers with ErrFailed.
func (s *Service) fail(ctx context.Context, f Flow, reason string, err error) error {
	attrs := []any{"intent", int(f.Intent), "reason", reason}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	s.d.Logger.WarnContext(ctx, "oidc flow failed", attrs...)
	return ErrFailed
}

// Complete finishes a flow when the provider returns with state and code. binding is the cookie
// of the browser, sessionToken its session cookie (for linking and step-up).
func (s *Service) Complete(ctx context.Context, state, code, binding, sessionToken, clientIP string) (Result, error) {
	if !s.d.Limiter.Allow(clientIP) {
		return Result{}, ErrTooManyAttempts
	}
	if state == "" || code == "" || binding == "" || len(state) > 128 || len(code) > 2048 || len(binding) > 128 {
		return Result{}, s.fail(ctx, Flow{}, "missing parameters", nil)
	}
	now := s.d.Now()
	f, ok, err := s.d.Flows.Take(ctx, hash(state), now)
	if err != nil {
		return Result{}, fmt.Errorf("oidc complete: %w", err)
	}
	if !ok {
		return Result{}, s.fail(ctx, f, "unknown or expired state", nil)
	}
	if subtle.ConstantTimeCompare([]byte(hash(binding)), []byte(f.BindingHash)) != 1 {
		return Result{}, s.fail(ctx, f, "the flow belongs to another browser", nil)
	}
	claims, err := s.d.Provider.Exchange(ctx, code, f.Verifier)
	if err != nil {
		return Result{}, s.fail(ctx, f, "code exchange or ID token", err)
	}
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(f.Nonce)) != 1 {
		return Result{}, s.fail(ctx, f, "nonce mismatch", nil)
	}
	switch f.Intent {
	case IntentLogin:
		return s.login(ctx, f, claims)
	case IntentLink, IntentStepUp:
		current, err := s.d.Sessions.Validate(ctx, sessionToken)
		if err != nil || current.ID != f.SessionID || current.User != f.User {
			return Result{}, s.fail(ctx, f, "not the session that started the flow", err)
		}
		if f.Intent == IntentLink {
			return s.link(ctx, f, claims)
		}
		return s.stepUp(ctx, f, claims, sessionToken)
	}
	return Result{}, s.fail(ctx, f, "unknown intent", nil)
}

func (s *Service) login(ctx context.Context, f Flow, c Claims) (Result, error) {
	user, found, err := s.d.Identities.Find(ctx, c.Issuer, c.Subject)
	if err != nil {
		return Result{}, fmt.Errorf("oidc login: %w", err)
	}
	provisioned := false
	if !found && s.d.JIT && s.d.Provisioner != nil {
		if user, found, err = s.d.Provisioner.Provision(ctx, c); err != nil {
			return Result{}, fmt.Errorf("oidc login: %w", err)
		}
		provisioned = found
	}
	if !found {
		s.d.Logger.WarnContext(ctx, "oidc login with an unknown identity", "issuer", c.Issuer)
		return Result{}, ErrUnknownIdentity
	}
	if err := s.d.Identities.Seen(ctx, c.Issuer, c.Subject, c.Email, s.d.Now()); err != nil {
		return Result{}, fmt.Errorf("oidc login: %w", err)
	}
	// The roles follow the groups before the session exists: a failure ends the sign-in rather
	// than leave roles the provider took away.
	if s.d.Provisioner != nil {
		// Only an account created just now joins organizations by the mappings; an existing one
		// only gets and loses the mapped roles where it is a member, so a removal by an admin
		// holds (M3-28).
		if err := s.d.Provisioner.Reconcile(ctx, user, c.Groups, provisioned); err != nil {
			return Result{}, fmt.Errorf("oidc login: %w", err)
		}
	}
	orgs, err := s.d.Users.Organizations(ctx, user)
	if err != nil {
		return Result{}, fmt.Errorf("oidc login: %w", err)
	}
	var active tenant.OrgID
	if len(orgs) > 0 {
		active = orgs[0].ID
	}
	mfa := s.mfa(c)
	if !mfa && s.d.SecondFactor != nil {
		enabled, err := s.d.SecondFactor.Enabled(ctx, user)
		if err != nil {
			return Result{}, fmt.Errorf("oidc login: %w", err)
		}
		if enabled {
			token, sess, err := s.d.Sessions.CreatePending(ctx, user, active)
			if err != nil {
				return Result{}, fmt.Errorf("oidc login: %w", err)
			}
			return Result{Intent: IntentLogin, Token: token, Session: sess, Pending: true, Next: f.Next}, nil
		}
	}
	token, sess, err := s.d.Sessions.CreateExternal(ctx, user, active, mfa)
	if err != nil {
		return Result{}, fmt.Errorf("oidc login: %w", err)
	}
	return Result{Intent: IntentLogin, Token: token, Session: sess, Next: f.Next}, nil
}

func (s *Service) link(ctx context.Context, f Flow, c Claims) (Result, error) {
	err := s.d.Identities.Link(ctx, Identity{Issuer: c.Issuer, Subject: c.Subject, User: f.User, Email: c.Email})
	if errors.Is(err, ErrIdentityTaken) {
		s.d.Logger.WarnContext(ctx, "oidc identity is linked already", "issuer", c.Issuer)
		return Result{}, ErrIdentityTaken
	}
	if err != nil {
		return Result{}, fmt.Errorf("oidc link: %w", err)
	}
	return Result{Intent: IntentLink, Next: f.Next}, nil
}

func (s *Service) stepUp(ctx context.Context, f Flow, c Claims, sessionToken string) (Result, error) {
	user, found, err := s.d.Identities.Find(ctx, c.Issuer, c.Subject)
	if err != nil {
		return Result{}, fmt.Errorf("oidc step-up: %w", err)
	}
	if !found || user != f.User {
		return Result{}, s.fail(ctx, f, "the identity is not the user's", nil)
	}
	// The provider was asked for a fresh login (max_age=0): an older one doesn't count.
	if c.AuthTime.IsZero() || c.AuthTime.Before(f.CreatedAt.Add(-authTimeSkew)) {
		return Result{}, s.fail(ctx, f, "no fresh login at the provider (auth_time)", nil)
	}
	// A user with a second factor needs one at the provider too: a provider password alone must
	// not stand in for the local factor (M3-28, like ConfirmPassword).
	if !s.mfa(c) && s.d.SecondFactor != nil {
		enabled, err := s.d.SecondFactor.Enabled(ctx, user)
		if err != nil {
			return Result{}, fmt.Errorf("oidc step-up: %w", err)
		}
		if enabled {
			return Result{}, s.fail(ctx, f, "provider login without a second factor for a user with one", nil)
		}
	}
	sess, err := s.d.Sessions.StepUp(ctx, sessionToken)
	if err != nil {
		return Result{}, fmt.Errorf("oidc step-up: %w", err)
	}
	return Result{Intent: IntentStepUp, Session: sess, Next: f.Next}, nil
}

// SignInWays tells the session view how the user signs in: with a password, with an identity of
// the provider (M3-22).
func (s *Service) SignInWays(ctx context.Context, user identity.UserID) (password, linked bool, err error) {
	if password, err = s.d.Identities.HasPassword(ctx, user); err != nil {
		return false, false, fmt.Errorf("sign-in ways: %w", err)
	}
	ids, err := s.d.Identities.OfUser(ctx, user)
	if err != nil {
		return false, false, fmt.Errorf("sign-in ways: %w", err)
	}
	return password, len(ids) > 0, nil
}

// Identities lists the identities of the session's user.
func (s *Service) Identities(ctx context.Context, sessionToken string) ([]LinkedIdentity, error) {
	current, err := s.d.Sessions.Validate(ctx, sessionToken)
	if err != nil {
		return nil, ErrNotSignedIn
	}
	ids, err := s.d.Identities.OfUser(ctx, current.User)
	if err != nil {
		return nil, fmt.Errorf("identities: %w", err)
	}
	return ids, nil
}

// Unlink removes an identity of the session's user after a recent step-up, as long as another
// way to sign in remains (a password or another identity).
func (s *Service) Unlink(ctx context.Context, sessionToken, issuer string) error {
	current, err := s.d.Sessions.Validate(ctx, sessionToken)
	if err != nil {
		return ErrNotSignedIn
	}
	if !current.SteppedUpWithin(access.StepUpMaxAge, s.d.Now()) {
		return access.ErrStepUpRequired
	}
	ids, err := s.d.Identities.OfUser(ctx, current.User)
	if err != nil {
		return fmt.Errorf("unlink: %w", err)
	}
	if !slices.ContainsFunc(ids, func(i LinkedIdentity) bool { return i.Issuer == issuer }) {
		return ErrNotLinked
	}
	password, err := s.d.Identities.HasPassword(ctx, current.User)
	if err != nil {
		return fmt.Errorf("unlink: %w", err)
	}
	if !password && len(ids) < 2 {
		return ErrLastSignIn
	}
	if err := s.d.Identities.Unlink(ctx, current.User, issuer); err != nil {
		return fmt.Errorf("unlink: %w", err)
	}
	return nil
}

// TrustsSecondFactor reports whether every login at the provider counts as two factors (the
// operator trusts it): then the step-up there also serves users with a local second factor.
func (s *Service) TrustsSecondFactor() bool { return s != nil && s.d.MFA == MFAAlways }
