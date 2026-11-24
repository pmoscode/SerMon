package oidc_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

var (
	t0     = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	alice  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	bob    = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	issuer = "https://idp.example.com"
)

// provider plays the identity provider: it remembers the last authorization request and answers
// the exchange with the claims set by the test (with the nonce of the request unless overridden).
type provider struct {
	mu        sync.Mutex
	nonce     string
	challenge string
	force     bool
	claims    oidc.Claims
	nonceOver *string
	err       error
	verifiers []string
}

func (p *provider) AuthCodeURL(_ context.Context, state, nonce, challenge string, force bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce, p.challenge, p.force = nonce, challenge, force
	return "https://idp.example.com/auth?state=" + url.QueryEscape(state), nil
}

func (p *provider) Exchange(_ context.Context, _, verifier string) (oidc.Claims, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verifiers = append(p.verifiers, verifier)
	c := p.claims
	c.Nonce = p.nonce
	if p.nonceOver != nil {
		c.Nonce = *p.nonceOver
	}
	return c, p.err
}

type flows struct {
	mu sync.Mutex
	m  map[string]oidc.Flow
}

func (f *flows) Insert(_ context.Context, fl oidc.Flow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[fl.StateHash] = fl
	return nil
}

func (f *flows) Take(_ context.Context, h string, now time.Time) (oidc.Flow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[h]
	delete(f.m, h)
	return fl, ok && now.Before(fl.ExpiresAt), nil
}

func (f *flows) DeleteExpired(context.Context, time.Time) error { return nil }

type identities struct {
	m      map[[2]string]identity.UserID
	seen   int
	linked []oidc.Identity
}

func (i *identities) Find(_ context.Context, iss, sub string) (identity.UserID, bool, error) {
	u, ok := i.m[[2]string{iss, sub}]
	return u, ok, nil
}

func (i *identities) Link(_ context.Context, id oidc.Identity) error {
	if _, ok := i.m[[2]string{id.Issuer, id.Subject}]; ok {
		return oidc.ErrIdentityTaken
	}
	i.m[[2]string{id.Issuer, id.Subject}] = id.User
	i.linked = append(i.linked, id)
	return nil
}

func (i *identities) Seen(context.Context, string, string, string, time.Time) error {
	i.seen++
	return nil
}

type users struct{}

func (users) Organizations(context.Context, identity.UserID) ([]auth.Organization, error) {
	return []auth.Organization{{ID: orgA, Name: "Alpha"}}, nil
}

type secondFactor map[identity.UserID]bool

func (s secondFactor) Enabled(_ context.Context, u identity.UserID) (bool, error) { return s[u], nil }

type memSessions struct {
	mu sync.Mutex
	m  map[string]session.Session
}

func (s *memSessions) Insert(_ context.Context, se session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[se.ID] = se
	return nil
}

func (s *memSessions) Get(_ context.Context, id string) (session.Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.m[id]
	return se, ok, nil
}
func (s *memSessions) Touch(context.Context, string, time.Time) error { return nil }
func (s *memSessions) SetStepUp(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	se := s.m[id]
	se.StepUpAt = at
	s.m[id] = se
	return nil
}

func (s *memSessions) Delete(_ context.Context, id string) error            { delete(s.m, id); return nil }
func (s *memSessions) DeleteForUser(context.Context, identity.UserID) error { return nil }

type env struct {
	svc      *oidc.Service
	idp      *provider
	ids      *identities
	sessions *session.Manager
	now      *time.Time
}

func newEnv(t *testing.T, mode oidc.MFAMode, local secondFactor) env {
	t.Helper()
	now := t0
	clock := func() time.Time { return now }
	idp := &provider{claims: oidc.Claims{Issuer: issuer, Subject: "alice-sub", Email: "alice@example.org", AuthTime: t0}}
	ids := &identities{m: map[[2]string]identity.UserID{{issuer, "alice-sub"}: alice}}
	sessions := session.NewManager(&memSessions{m: map[string]session.Session{}}, clock)
	svc := oidc.NewService(oidc.Deps{
		Provider: idp, Flows: &flows{m: map[string]oidc.Flow{}}, Identities: ids, Users: users{}, Sessions: sessions,
		SecondFactor: local, Limiter: ratelimit.New(50, time.Minute, clock), MFA: mode, Now: clock,
	})
	return env{svc: svc, idp: idp, ids: ids, sessions: sessions, now: &now}
}

func stateOf(t *testing.T, b oidc.Begun) string {
	t.Helper()
	u, err := url.Parse(b.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func TestLogin_WithAKnownIdentity(t *testing.T) {
	e := newEnv(t, oidc.MFAByClaim, nil)
	b, err := e.svc.Begin(context.Background(), oidc.IntentLogin, "", "/alerts?tab=rules", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if e.idp.force || len(e.idp.challenge) != 43 || b.Binding == "" {
		t.Errorf("authorization request: force %v, challenge %q", e.idp.force, e.idp.challenge)
	}
	res, err := e.svc.Complete(context.Background(), stateOf(t, b), "code", b.Binding, "", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent != oidc.IntentLogin || res.Token == "" || res.Pending || res.Session.User != alice || res.Session.Org != orgA ||
		res.Session.ExternalMFA || res.Next != "/alerts?tab=rules" || e.ids.seen != 1 {
		t.Errorf("result %+v", res)
	}
	// The verifier sent with the code belongs to the challenge (S256).
	if v := e.idp.verifiers[0]; len(v) < 43 {
		t.Errorf("verifier %q", v)
	}
	if _, err := e.sessions.Validate(context.Background(), res.Token); err != nil {
		t.Errorf("the session is not valid: %v", err)
	}
	// The state works once.
	if _, err := e.svc.Complete(context.Background(), stateOf(t, b), "code", b.Binding, "", "192.0.2.1"); !errors.Is(err, oidc.ErrFailed) {
		t.Errorf("the state again: %v", err)
	}
}

func TestLogin_Refusals(t *testing.T) {
	other := "other-nonce"
	for name, tc := range map[string]struct {
		change func(e env, b *oidc.Begun, state *string)
		want   error
	}{
		"wrong state":           {func(_ env, _ *oidc.Begun, s *string) { *s = "forged" }, oidc.ErrFailed},
		"another browser":       {func(_ env, b *oidc.Begun, _ *string) { b.Binding = "the attacker's" }, oidc.ErrFailed},
		"wrong nonce":           {func(e env, _ *oidc.Begun, _ *string) { e.idp.nonceOver = &other }, oidc.ErrFailed},
		"token refused":         {func(e env, _ *oidc.Begun, _ *string) { e.idp.err = errors.New("audience mismatch") }, oidc.ErrFailed},
		"expired flow":          {func(e env, _ *oidc.Begun, _ *string) { *e.now = e.now.Add(oidc.FlowLifetime + time.Second) }, oidc.ErrFailed},
		"unknown identity":      {func(e env, _ *oidc.Begun, _ *string) { e.idp.claims.Subject = "mallory" }, oidc.ErrUnknownIdentity},
		"same subject, another": {func(e env, _ *oidc.Begun, _ *string) { e.idp.claims.Issuer = "https://evil.example" }, oidc.ErrUnknownIdentity},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, oidc.MFAByClaim, nil)
			b, err := e.svc.Begin(context.Background(), oidc.IntentLogin, "", "/", "192.0.2.1")
			if err != nil {
				t.Fatal(err)
			}
			state := stateOf(t, b)
			tc.change(e, &b, &state)
			res, err := e.svc.Complete(context.Background(), state, "code", b.Binding, "", "192.0.2.1")
			if !errors.Is(err, tc.want) || res.Token != "" {
				t.Errorf("Complete() = %+v, %v; want %v", res, err, tc.want)
			}
		})
	}
	e := newEnv(t, oidc.MFAByClaim, nil)
	for _, missing := range [][3]string{{"", "c", "b"}, {"s", "", "b"}, {"s", "c", ""}} {
		if _, err := e.svc.Complete(context.Background(), missing[0], missing[1], missing[2], "", "192.0.2.1"); !errors.Is(err, oidc.ErrFailed) {
			t.Errorf("missing %v: %v", missing, err)
		}
	}
}

// The organization's two-factor requirement: amr naming a second factor (or the operator's trust)
// counts; otherwise a user with a local second factor still enters it.
func TestLogin_SecondFactors(t *testing.T) {
	for name, tc := range map[string]struct {
		mode        oidc.MFAMode
		amr         []string
		local       bool
		wantMFA     bool
		wantPending bool
	}{
		"amr with otp":                    {oidc.MFAByClaim, []string{"pwd", "otp"}, true, true, false},
		"amr password only, local 2FA":    {oidc.MFAByClaim, []string{"pwd"}, true, false, true},
		"amr password only, no local":     {oidc.MFAByClaim, []string{"pwd"}, false, false, false},
		"trusted provider":                {oidc.MFAAlways, nil, true, true, false},
		"never trusted, amr says mfa":     {oidc.MFANever, []string{"mfa"}, true, false, true},
		"never trusted, without local":    {oidc.MFANever, []string{"mfa"}, false, false, false},
		"an unknown amr is no 2nd factor": {oidc.MFAByClaim, []string{"face-ish"}, false, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, tc.mode, secondFactor{alice: tc.local})
			e.idp.claims.AMR = tc.amr
			b, _ := e.svc.Begin(context.Background(), oidc.IntentLogin, "", "/", "192.0.2.1")
			res, err := e.svc.Complete(context.Background(), stateOf(t, b), "code", b.Binding, "", "192.0.2.1")
			if err != nil {
				t.Fatal(err)
			}
			if res.Session.ExternalMFA != tc.wantMFA || res.Pending != tc.wantPending || res.Session.SecondFactorPending != tc.wantPending {
				t.Errorf("result %+v", res)
			}
		})
	}
}

func signedIn(t *testing.T, e env, user identity.UserID) string {
	t.Helper()
	token, _, err := e.sessions.Create(context.Background(), user, orgA)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestLink_OnlySignedInWithAStepUp(t *testing.T) {
	e := newEnv(t, oidc.MFAByClaim, nil)
	ctx := context.Background()
	if _, err := e.svc.Begin(ctx, oidc.IntentLink, "", "/settings/security", "192.0.2.1"); !errors.Is(err, oidc.ErrNotSignedIn) {
		t.Errorf("without a session: %v", err)
	}
	token := signedIn(t, e, bob)
	*e.now = e.now.Add(20 * time.Minute)
	if _, err := e.svc.Begin(ctx, oidc.IntentLink, token, "/", "192.0.2.1"); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("an old login: %v", err)
	}
	token = signedIn(t, e, bob)
	b, err := e.svc.Begin(ctx, oidc.IntentLink, token, "/settings/security", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	e.idp.claims.Subject = "bob-sub"
	// Another session (another browser tab of someone else) can't finish it.
	if _, err := e.svc.Complete(ctx, stateOf(t, b), "code", b.Binding, signedIn(t, e, bob), "192.0.2.1"); !errors.Is(err, oidc.ErrFailed) {
		t.Errorf("another session: %v", err)
	}
	b, _ = e.svc.Begin(ctx, oidc.IntentLink, token, "/settings/security", "192.0.2.1")
	res, err := e.svc.Complete(ctx, stateOf(t, b), "code", b.Binding, token, "192.0.2.1")
	if err != nil || res.Intent != oidc.IntentLink || res.Token != "" || res.Next != "/settings/security" {
		t.Fatalf("link = %+v, %v", res, err)
	}
	if len(e.ids.linked) != 1 || e.ids.linked[0].User != bob || e.ids.linked[0].Subject != "bob-sub" {
		t.Errorf("linked %+v", e.ids.linked)
	}
	// Alice's identity can't be taken over.
	e.idp.claims.Subject = "alice-sub"
	b, _ = e.svc.Begin(ctx, oidc.IntentLink, token, "/", "192.0.2.1")
	if _, err := e.svc.Complete(ctx, stateOf(t, b), "code", b.Binding, token, "192.0.2.1"); !errors.Is(err, oidc.ErrIdentityTaken) {
		t.Errorf("someone else's identity: %v", err)
	}
}

func TestStepUp_NeedsAFreshLoginOfTheSameUser(t *testing.T) {
	e := newEnv(t, oidc.MFAByClaim, nil)
	ctx := context.Background()
	token := signedIn(t, e, alice)
	*e.now = e.now.Add(20 * time.Minute)
	b, err := e.svc.Begin(ctx, oidc.IntentStepUp, token, "/admin", "192.0.2.1")
	if err != nil || !e.idp.force {
		t.Fatalf("Begin() = %v, force %v", err, e.idp.force)
	}
	// The provider reused an old login.
	e.idp.claims.AuthTime = t0
	if _, err := e.svc.Complete(ctx, stateOf(t, b), "code", b.Binding, token, "192.0.2.1"); !errors.Is(err, oidc.ErrFailed) {
		t.Errorf("an old auth_time: %v", err)
	}
	// Another user's identity.
	e.ids.m[[2]string{issuer, "bob-sub"}] = bob
	e.idp.claims = oidc.Claims{Issuer: issuer, Subject: "bob-sub", AuthTime: *e.now}
	b, _ = e.svc.Begin(ctx, oidc.IntentStepUp, token, "/admin", "192.0.2.1")
	if _, err := e.svc.Complete(ctx, stateOf(t, b), "code", b.Binding, token, "192.0.2.1"); !errors.Is(err, oidc.ErrFailed) {
		t.Errorf("another user: %v", err)
	}
	e.idp.claims = oidc.Claims{Issuer: issuer, Subject: "alice-sub", AuthTime: e.now.Add(time.Second)}
	b, _ = e.svc.Begin(ctx, oidc.IntentStepUp, token, "/admin", "192.0.2.1")
	res, err := e.svc.Complete(ctx, stateOf(t, b), "code", b.Binding, token, "192.0.2.1")
	if err != nil || res.Intent != oidc.IntentStepUp || !res.Session.StepUpAt.Equal(*e.now) || res.Next != "/admin" {
		t.Errorf("step-up = %+v, %v", res, err)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, oidc.MFAByClaim, nil)
	var err error
	for range 60 {
		if _, err = e.svc.Begin(context.Background(), oidc.IntentLogin, "", "/", "198.51.100.9"); err != nil {
			break
		}
	}
	if !errors.Is(err, oidc.ErrTooManyAttempts) {
		t.Errorf("after 60 attempts: %v", err)
	}
}

func TestSafeNext(t *testing.T) {
	for raw, want := range map[string]string{
		"/alerts":                      "/alerts",
		"/logs?node=x&range=1h":        "/logs?node=x&range=1h",
		"/settings/security":           "/settings/security",
		"":                             "/",
		"https://evil.example":         "/",
		"//evil.example":               "/",
		"/\\evil.example":              "/",
		"/a/../../etc":                 "/",
		"/./x":                         "/",
		"/x#frag":                      "/",
		"alerts":                       "/",
		"/x\n":                         "/",
		"/" + strings.Repeat("a", 600): "/",
		"/%2F%2Fevil.example":          "/",
	} {
		if got := oidc.SafeNext(raw); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", raw, got, want)
		}
	}
}

// provisioner records what a sign-in asks of the mappings (M3-21).
type provisioner struct {
	created   identity.UserID
	reconcile []string
	user      identity.UserID
	join      bool
	calls     int
	err       error
}

func (p *provisioner) Reconcile(_ context.Context, user identity.UserID, groups []string, join bool) error {
	p.calls++
	p.user, p.reconcile, p.join = user, groups, join
	return p.err
}

func (p *provisioner) Provision(context.Context, oidc.Claims) (identity.UserID, bool, error) {
	return p.created, !p.created.IsZero(), nil
}

func withProvisioner(t *testing.T, p *provisioner, jit bool) env {
	t.Helper()
	now := t0
	clock := func() time.Time { return now }
	idp := &provider{claims: oidc.Claims{Issuer: issuer, Subject: "alice-sub", Groups: []string{"ops"}}}
	ids := &identities{m: map[[2]string]identity.UserID{{issuer, "alice-sub"}: alice}}
	sessions := session.NewManager(&memSessions{m: map[string]session.Session{}}, clock)
	svc := oidc.NewService(oidc.Deps{
		Provider: idp, Flows: &flows{m: map[string]oidc.Flow{}}, Identities: ids, Users: users{}, Sessions: sessions,
		Limiter: ratelimit.New(50, time.Minute, clock), Provisioner: p, JIT: jit, Now: clock,
	})
	return env{svc: svc, idp: idp, ids: ids, sessions: sessions, now: &now}
}

func complete(t *testing.T, e env) (oidc.Result, error) {
	t.Helper()
	b, err := e.svc.Begin(context.Background(), oidc.IntentLogin, "", "/", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	return e.svc.Complete(context.Background(), stateOf(t, b), "code", b.Binding, "", "192.0.2.1")
}

// M3-21: every sign-in reconciles the roles with the groups; JIT creates unknown accounts.
func TestLogin_ReconcilesAndProvisions(t *testing.T) {
	p := &provisioner{}
	e := withProvisioner(t, p, true)
	res, err := complete(t, e)
	if err != nil || res.Session.User != alice || p.calls != 1 || p.user != alice || !p.join || strings.Join(p.reconcile, ",") != "ops" {
		t.Fatalf("known identity: %+v, %v, %+v", res, err, p)
	}
	// Without JIT the mappings still give and take roles, but nobody joins.
	p = &provisioner{created: bob}
	e = withProvisioner(t, p, false)
	if _, err := complete(t, e); err != nil || p.join {
		t.Errorf("without JIT: %v, join %v", err, p.join)
	}
	e.idp.claims.Subject = "new-sub"
	if _, err := complete(t, e); !errors.Is(err, oidc.ErrUnknownIdentity) {
		t.Errorf("an unknown identity without JIT: %v", err)
	}
	// With JIT an unknown identity gets the account the mappings allow.
	p = &provisioner{created: bob}
	e = withProvisioner(t, p, true)
	e.idp.claims.Subject = "new-sub"
	res, err = complete(t, e)
	if err != nil || res.Session.User != bob || p.user != bob {
		t.Errorf("provisioned: %+v, %v", res, err)
	}
	// No mapping matches: nothing is created.
	e = withProvisioner(t, &provisioner{}, true)
	e.idp.claims.Subject = "new-sub"
	if _, err := complete(t, e); !errors.Is(err, oidc.ErrUnknownIdentity) {
		t.Errorf("nothing provisioned: %v", err)
	}
	// A failed reconciliation ends the sign-in: no session with roles the provider took away.
	p = &provisioner{err: errors.New("database down")}
	e = withProvisioner(t, p, true)
	if res, err := complete(t, e); err == nil || res.Token != "" {
		t.Errorf("failed reconciliation: %+v, %v", res, err)
	}
}
