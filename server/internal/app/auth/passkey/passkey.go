// Package passkey implements passkeys (WebAuthn, M3-23, ADR 0034): registration after a step-up,
// sign-in without a password (a discoverable credential with user verification counts as two
// factors), the second factor after the password and the step-up. Every ceremony works once
// within five minutes and is bound to the session or, for the sign-in, to a cookie.
package passkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Purpose is what a ceremony is for.
type Purpose int

// Purposes of a ceremony.
const (
	PurposeRegister Purpose = iota + 1
	PurposeLogin
	PurposeSecondFactor
	PurposeStepUp
)

// Bounds.
const (
	MaxPerUser       = 10
	MaxName          = 64
	CeremonyLifetime = 5 * time.Minute
	// MaxResponse bounds the JSON a browser sends back.
	MaxResponse = 64 << 10
)

var (
	// ErrFailed covers every failed ceremony alike (the reason is logged).
	ErrFailed = errors.New("the passkey was not accepted")
	// ErrTooMany means the user has MaxPerUser passkeys.
	ErrTooMany = fmt.Errorf("at most %d passkeys per account", MaxPerUser)
	// ErrInvalidName means the name is empty, too long or has control characters.
	ErrInvalidName = fmt.Errorf("the name needs 1 to %d characters without control characters", MaxName)
	// ErrNotFound means the user has no such passkey.
	ErrNotFound = errors.New("no such passkey")
	// ErrNotSignedIn means the purpose needs a (waiting) session.
	ErrNotSignedIn = errors.New("sign in first")
	// ErrTooManyAttempts means the client IP exceeded the rate limit.
	ErrTooManyAttempts = errors.New("too many attempts, please wait a moment")
	// ErrUnavailable means passkeys are off (no allowed origin).
	ErrUnavailable = errors.New("passkeys are not available on this server")
)

// Passkey is a credential of a user.
type Passkey struct {
	ID         []byte
	User       identity.UserID
	Name       string
	Credential webauthn.Credential
	CreatedAt  time.Time
	LastUsedAt time.Time // zero: never used
}

// Ceremony is a started registration or assertion.
type Ceremony struct {
	IDHash      string
	BindingHash string // sign-in without a session: the cookie of the browser
	Purpose     Purpose
	User        identity.UserID
	SessionID   string
	Name        string // registration: the name of the new passkey
	Data        webauthn.SessionData
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Store keeps passkeys and ceremonies (instance-level, like the credentials).
type Store interface {
	List(ctx context.Context, user identity.UserID) ([]Passkey, error)
	Find(ctx context.Context, id []byte) (Passkey, bool, error)
	// Add stores a passkey with an entry in the instance audit log; ErrTooMany beyond limit,
	// ErrFailed if the credential exists.
	Add(ctx context.Context, p Passkey, limit int) error
	// Used stores the credential after an assertion (counter, flags) and the time.
	Used(ctx context.Context, id []byte, c webauthn.Credential, at time.Time) error
	// CloneSuspected records a refused assertion whose counter went back in the instance audit
	// log (M3-28).
	CloneSuspected(ctx context.Context, user identity.UserID, id []byte) error
	Rename(ctx context.Context, user identity.UserID, id []byte, name string) error
	// Delete removes a passkey with an entry in the instance audit log; ErrNotFound if the user has
	// none with id.
	Delete(ctx context.Context, user identity.UserID, id []byte) error
	InsertCeremony(ctx context.Context, c Ceremony) error
	// TakeCeremony returns and deletes the ceremony if it has not expired at now.
	TakeCeremony(ctx context.Context, idHash string, now time.Time) (Ceremony, bool, error)
	DeleteExpiredCeremonies(ctx context.Context, now time.Time) error
}

// Users answers what the ceremonies need to know of a user.
type Users interface {
	Profile(ctx context.Context, user identity.UserID) (auth.Profile, bool, error)
	Organizations(ctx context.Context, user identity.UserID) ([]auth.Organization, error)
}

// Lockout runs a check under the account's lockout (auth.Service.VerifyFactor).
type Lockout interface {
	VerifyFactor(ctx context.Context, user identity.UserID, check func() (bool, error)) error
}

// RateLimiter limits attempts per client IP.
type RateLimiter interface {
	Allow(key string) bool
}

// Deps are the collaborators of the Service.
type Deps struct {
	// WebAuthn is the relying party; nil: passkeys are off.
	WebAuthn *webauthn.WebAuthn
	Store    Store
	Users    Users
	Lockout  Lockout
	Sessions *session.Manager
	Limiter  RateLimiter
	Now      func() time.Time
	Logger   *slog.Logger
}

// Service runs the ceremonies.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}
}

// Available reports whether passkeys are on.
func (s *Service) Available() bool { return s != nil && s.d.WebAuthn != nil }

// Config is the relying party from the allowed origins: the host of the first is the RP ID, the
// origins on it or below it are allowed.
func Config(origins []string) (*webauthn.Config, error) {
	if len(origins) == 0 {
		return nil, ErrUnavailable
	}
	first, err := hostOf(origins[0])
	if err != nil {
		return nil, err
	}
	cfg := &webauthn.Config{RPID: first, RPDisplayName: "SerMon"}
	for _, o := range origins {
		h, err := hostOf(o)
		if err == nil && (h == first || strings.HasSuffix(h, "."+first)) {
			cfg.RPOrigins = append(cfg.RPOrigins, o)
		}
	}
	return cfg, nil
}

func hostOf(origin string) (string, error) {
	_, rest, ok := strings.Cut(origin, "://")
	if !ok || rest == "" {
		return "", fmt.Errorf("passkey: origin %q has no host", origin)
	}
	host := rest
	if h, _, found := strings.Cut(rest, "/"); found {
		host = h
	}
	if strings.HasPrefix(host, "[") {
		return "", fmt.Errorf("passkey: origin %q is an IP address", origin)
	}
	if h, _, found := strings.Cut(host, ":"); found {
		host = h
	}
	return strings.ToLower(host), nil
}

// user adapts a user to go-webauthn.
type user struct {
	id          identity.UserID
	name, label string
	creds       []webauthn.Credential
}

func (u user) WebAuthnID() []byte                         { b := uuid.UUID(u.id); return b[:] }
func (u user) WebAuthnName() string                       { return u.name }
func (u user) WebAuthnDisplayName() string                { return u.label }
func (u user) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Service) load(ctx context.Context, id identity.UserID) (user, []Passkey, error) {
	profile, _, err := s.d.Users.Profile(ctx, id)
	if err != nil {
		return user{}, nil, fmt.Errorf("profile: %w", err)
	}
	keys, err := s.d.Store.List(ctx, id)
	if err != nil {
		return user{}, nil, fmt.Errorf("passkeys: %w", err)
	}
	u := user{id: id, name: profile.Email, label: profile.DisplayName}
	for _, k := range keys {
		u.creds = append(u.creds, k.Credential)
	}
	return u, keys, nil
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// List returns the passkeys of a user.
func (s *Service) List(ctx context.Context, u identity.UserID) ([]Passkey, error) {
	keys, err := s.d.Store.List(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	return keys, nil
}

// Count returns how many passkeys a user has (second factors, twofactor.Service).
func (s *Service) Count(ctx context.Context, u identity.UserID) (int, error) {
	keys, err := s.List(ctx, u)
	return len(keys), err
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxName || !utf8.ValidString(name) ||
		strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", ErrInvalidName
	}
	return name, nil
}

// Begun is a started ceremony: the browser passes Options to navigator.credentials and sends the
// answer back with CeremonyID; Binding goes into a cookie (sign-in only).
type Begun struct {
	CeremonyID string
	Options    json.RawMessage
	Binding    string
}

func (s *Service) store(ctx context.Context, c Ceremony) (string, error) {
	id, err := random()
	if err != nil {
		return "", err
	}
	now := s.d.Now()
	c.IDHash, c.CreatedAt, c.ExpiresAt = hash(id), now, now.Add(CeremonyLifetime)
	if err := s.d.Store.DeleteExpiredCeremonies(ctx, now); err != nil {
		return "", fmt.Errorf("passkey ceremony: %w", err)
	}
	if err := s.d.Store.InsertCeremony(ctx, c); err != nil {
		return "", fmt.Errorf("passkey ceremony: %w", err)
	}
	return id, nil
}

// BeginRegistration starts adding a passkey to the session's user (recent step-up).
func (s *Service) BeginRegistration(ctx context.Context, current session.Session, name string) (Begun, error) {
	if !s.Available() {
		return Begun{}, ErrUnavailable
	}
	if !current.SteppedUpWithin(access.StepUpMaxAge, s.d.Now()) {
		return Begun{}, access.ErrStepUpRequired
	}
	name, err := validName(name)
	if err != nil {
		return Begun{}, err
	}
	u, keys, err := s.load(ctx, current.User)
	if err != nil {
		return Begun{}, err
	}
	if len(keys) >= MaxPerUser {
		return Begun{}, ErrTooMany
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for i := range u.creds {
		exclude = append(exclude, u.creds[i].Descriptor())
	}
	creation, data, err := s.d.WebAuthn.BeginRegistration(u,
		webauthn.WithExclusions(exclude),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementPreferred, UserVerification: protocol.VerificationPreferred,
		}))
	if err != nil {
		return Begun{}, fmt.Errorf("begin passkey registration: %w", err)
	}
	return s.begun(ctx, creation, Ceremony{Purpose: PurposeRegister, User: current.User, SessionID: current.ID, Name: name, Data: *data})
}

func (s *Service) begun(ctx context.Context, options any, c Ceremony) (Begun, error) {
	raw, err := json.Marshal(options)
	if err != nil {
		return Begun{}, fmt.Errorf("passkey options: %w", err)
	}
	id, err := s.store(ctx, c)
	if err != nil {
		return Begun{}, err
	}
	return Begun{CeremonyID: id, Options: raw}, nil
}

// take returns the ceremony of id for purpose; ok false for anything else (expired, used, other
// purpose).
func (s *Service) take(ctx context.Context, id string, purpose Purpose) (Ceremony, bool, error) {
	if id == "" || len(id) > 128 {
		return Ceremony{}, false, nil
	}
	c, ok, err := s.d.Store.TakeCeremony(ctx, hash(id), s.d.Now())
	if err != nil {
		return Ceremony{}, false, fmt.Errorf("passkey ceremony: %w", err)
	}
	return c, ok && c.Purpose == purpose, nil
}

func (s *Service) fail(ctx context.Context, purpose Purpose, reason string, err error) error {
	attrs := []any{"purpose", int(purpose), "reason", reason}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	s.d.Logger.WarnContext(ctx, "passkey ceremony failed", attrs...)
	return ErrFailed
}

// FinishRegistration stores the new passkey of the session's user.
func (s *Service) FinishRegistration(ctx context.Context, current session.Session, ceremonyID string, response []byte) (Passkey, error) {
	if !s.Available() {
		return Passkey{}, ErrUnavailable
	}
	if !current.SteppedUpWithin(access.StepUpMaxAge, s.d.Now()) {
		return Passkey{}, access.ErrStepUpRequired
	}
	c, ok, err := s.take(ctx, ceremonyID, PurposeRegister)
	if err != nil {
		return Passkey{}, err
	}
	if !ok || c.SessionID != current.ID || c.User != current.User {
		return Passkey{}, s.fail(ctx, PurposeRegister, "unknown, used or foreign ceremony", nil)
	}
	if len(response) > MaxResponse {
		return Passkey{}, s.fail(ctx, PurposeRegister, "response too large", nil)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return Passkey{}, s.fail(ctx, PurposeRegister, "unparsable response", err)
	}
	u, _, err := s.load(ctx, current.User)
	if err != nil {
		return Passkey{}, err
	}
	cred, err := s.d.WebAuthn.CreateCredential(u, c.Data, parsed)
	if err != nil {
		return Passkey{}, s.fail(ctx, PurposeRegister, "credential refused", err)
	}
	now := s.d.Now()
	p := Passkey{ID: cred.ID, User: current.User, Name: c.Name, Credential: *cred, CreatedAt: now}
	if err := s.d.Store.Add(ctx, p, MaxPerUser); err != nil {
		if errors.Is(err, ErrTooMany) {
			return Passkey{}, ErrTooMany
		}
		if errors.Is(err, ErrFailed) {
			return Passkey{}, s.fail(ctx, PurposeRegister, "credential exists", nil)
		}
		return Passkey{}, fmt.Errorf("add passkey: %w", err)
	}
	return p, nil
}

// Rename names a passkey of the user anew.
func (s *Service) Rename(ctx context.Context, u identity.UserID, id []byte, name string) error {
	name, err := validName(name)
	if err != nil {
		return err
	}
	if err := s.d.Store.Rename(ctx, u, id, name); err != nil {
		return fmt.Errorf("rename passkey: %w", err)
	}
	return nil
}

// Delete removes a passkey of the session's user (recent step-up).
func (s *Service) Delete(ctx context.Context, current session.Session, id []byte) error {
	if !current.SteppedUpWithin(access.StepUpMaxAge, s.d.Now()) {
		return access.ErrStepUpRequired
	}
	if err := s.d.Store.Delete(ctx, current.User, id); err != nil {
		return fmt.Errorf("delete passkey: %w", err)
	}
	return nil
}

// Begin starts an assertion: a sign-in without a session, the second factor of a session that
// waits for it, or the step-up of a session.
func (s *Service) Begin(ctx context.Context, purpose Purpose, token, clientIP string) (Begun, error) {
	if !s.Available() {
		return Begun{}, ErrUnavailable
	}
	if !s.d.Limiter.Allow(clientIP) {
		return Begun{}, ErrTooManyAttempts
	}
	switch purpose {
	case PurposeLogin:
		assertion, data, err := s.d.WebAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return Begun{}, fmt.Errorf("begin passkey sign-in: %w", err)
		}
		binding, err := random()
		if err != nil {
			return Begun{}, err
		}
		b, err := s.begun(ctx, assertion, Ceremony{Purpose: purpose, BindingHash: hash(binding), Data: *data})
		b.Binding = binding
		return b, err
	case PurposeSecondFactor, PurposeStepUp:
		var current session.Session
		var err error
		if purpose == PurposeSecondFactor {
			current, err = s.d.Sessions.ValidatePending(ctx, token)
		} else {
			current, err = s.d.Sessions.Validate(ctx, token)
		}
		if err != nil {
			return Begun{}, ErrNotSignedIn
		}
		u, keys, err := s.load(ctx, current.User)
		if err != nil {
			return Begun{}, err
		}
		if len(keys) == 0 {
			return Begun{}, ErrNotFound
		}
		assertion, data, err := s.d.WebAuthn.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationPreferred))
		if err != nil {
			return Begun{}, fmt.Errorf("begin passkey assertion: %w", err)
		}
		return s.begun(ctx, assertion, Ceremony{Purpose: purpose, User: current.User, SessionID: current.ID, Data: *data})
	case PurposeRegister:
	}
	return Begun{}, fmt.Errorf("%w: unknown purpose", ErrFailed)
}

// Result is a completed assertion: a sign-in or second factor has Token (a full session), a
// step-up renewed the session of the cookie.
type Result struct {
	Purpose       Purpose
	Token         string
	Session       session.Session
	Organizations []auth.Organization
}

// Finish completes an assertion. binding is the cookie of the browser (sign-in), token the
// session cookie (second factor, step-up).
func (s *Service) Finish(ctx context.Context, purpose Purpose, ceremonyID, binding, token string, response []byte, clientIP string) (Result, error) {
	if !s.Available() {
		return Result{}, ErrUnavailable
	}
	if !s.d.Limiter.Allow(clientIP) {
		return Result{}, ErrTooManyAttempts
	}
	c, ok, err := s.take(ctx, ceremonyID, purpose)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, s.fail(ctx, purpose, "unknown, used or expired ceremony", nil)
	}
	if len(response) > MaxResponse {
		return Result{}, s.fail(ctx, purpose, "response too large", nil)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return Result{}, s.fail(ctx, purpose, "unparsable response", err)
	}
	switch purpose {
	case PurposeLogin:
		if subtle.ConstantTimeCompare([]byte(hash(binding)), []byte(c.BindingHash)) != 1 {
			return Result{}, s.fail(ctx, purpose, "the ceremony belongs to another browser", nil)
		}
		return s.login(ctx, c, parsed)
	case PurposeSecondFactor, PurposeStepUp:
		var current session.Session
		if purpose == PurposeSecondFactor {
			current, err = s.d.Sessions.ValidatePending(ctx, token)
		} else {
			current, err = s.d.Sessions.Validate(ctx, token)
		}
		if err != nil || current.ID != c.SessionID || current.User != c.User {
			return Result{}, s.fail(ctx, purpose, "not the session that began the ceremony", err)
		}
		if err := s.assert(ctx, c, parsed); err != nil {
			return Result{}, err
		}
		if purpose == PurposeStepUp {
			stepped, err := s.d.Sessions.StepUp(ctx, token)
			if err != nil {
				return Result{}, fmt.Errorf("passkey step-up: %w", err)
			}
			return Result{Purpose: purpose, Session: stepped}, nil
		}
		full, sess, err := s.d.Sessions.CompleteSecondFactor(ctx, token)
		if err != nil {
			return Result{}, fmt.Errorf("passkey second factor: %w", err)
		}
		return s.withOrganizations(ctx, Result{Purpose: purpose, Token: full, Session: sess})
	case PurposeRegister:
	}
	return Result{}, s.fail(ctx, purpose, "unknown purpose", nil)
}

func (s *Service) withOrganizations(ctx context.Context, r Result) (Result, error) {
	orgs, err := s.d.Users.Organizations(ctx, r.Session.User)
	if err != nil {
		return Result{}, fmt.Errorf("passkey: %w", err)
	}
	r.Organizations = orgs
	return r, nil
}

// assert checks an assertion of the ceremony's user under the account's lockout.
func (s *Service) assert(ctx context.Context, c Ceremony, parsed *protocol.ParsedCredentialAssertionData) error {
	u, _, err := s.load(ctx, c.User)
	if err != nil {
		return err
	}
	var failure error
	err = s.d.Lockout.VerifyFactor(ctx, c.User, func() (bool, error) {
		cred, err := s.d.WebAuthn.ValidateLogin(u, c.Data, parsed)
		if err != nil {
			failure = err
			return false, nil //nolint:nilerr // a refused assertion is a failed attempt, logged below
		}
		return s.accept(ctx, cred)
	})
	if err != nil {
		if errors.Is(err, auth.ErrWrongCode) {
			return s.fail(ctx, c.Purpose, "assertion refused", failure)
		}
		return fmt.Errorf("passkey: %w", err)
	}
	return nil
}

// accept stores the use of a credential, unless its counter went back (a clone).
func (s *Service) accept(ctx context.Context, cred *webauthn.Credential) (bool, error) {
	if cred.Authenticator.CloneWarning {
		s.d.Logger.WarnContext(ctx, "passkey refused: its signature counter went back – the authenticator may be cloned",
			"credential", base64.RawURLEncoding.EncodeToString(cred.ID))
		p, found, err := s.d.Store.Find(ctx, cred.ID)
		if err != nil {
			return false, fmt.Errorf("passkey clone: %w", err)
		}
		if found {
			if err := s.d.Store.CloneSuspected(ctx, p.User, cred.ID); err != nil {
				return false, fmt.Errorf("passkey clone: %w", err)
			}
		}
		return false, nil
	}
	if err := s.d.Store.Used(ctx, cred.ID, *cred, s.d.Now()); err != nil {
		return false, fmt.Errorf("passkey used: %w", err)
	}
	return true, nil
}

func (s *Service) login(ctx context.Context, c Ceremony, parsed *protocol.ParsedCredentialAssertionData) (Result, error) {
	var owner identity.UserID
	var failure error
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		p, found, err := s.d.Store.Find(ctx, rawID)
		if err != nil {
			return nil, fmt.Errorf("find passkey: %w", err)
		}
		id := uuid.UUID(p.User)
		if !found || !bytes.Equal(userHandle, id[:]) {
			return nil, errors.New("unknown passkey or another user")
		}
		owner = p.User
		u, _, err := s.load(ctx, p.User)
		return u, err
	}
	// Who it is follows from the credential; the lockout of that account applies.
	if _, err := handler(parsed.RawID, parsed.Response.UserHandle); err != nil {
		return Result{}, s.fail(ctx, PurposeLogin, "unknown passkey", err)
	}
	err := s.d.Lockout.VerifyFactor(ctx, owner, func() (bool, error) {
		_, cred, err := s.d.WebAuthn.ValidatePasskeyLogin(handler, c.Data, parsed)
		if err != nil {
			failure = err
			return false, nil //nolint:nilerr // a refused assertion is a failed attempt, logged below
		}
		return s.accept(ctx, cred)
	})
	if err != nil {
		if errors.Is(err, auth.ErrWrongCode) {
			return Result{}, s.fail(ctx, PurposeLogin, "assertion refused", failure)
		}
		return Result{}, fmt.Errorf("passkey sign-in: %w", err)
	}
	orgs, err := s.d.Users.Organizations(ctx, owner)
	if err != nil {
		return Result{}, fmt.Errorf("passkey sign-in: %w", err)
	}
	var active tenant.OrgID
	if len(orgs) > 0 {
		active = orgs[0].ID
	}
	// With user verification the passkey is two factors (ADR 0034).
	token, sess, err := s.d.Sessions.CreateExternal(ctx, owner, active, true)
	if err != nil {
		return Result{}, fmt.Errorf("passkey sign-in: %w", err)
	}
	return Result{Purpose: PurposeLogin, Token: token, Session: sess, Organizations: orgs}, nil
}
