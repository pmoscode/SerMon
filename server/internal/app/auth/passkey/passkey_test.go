package passkey_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey/passkeytest"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

const origin = "https://sermon.example.com"

var (
	t0    = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	alice = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	bob   = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
)

type store struct {
	clones     int
	mu         sync.Mutex
	keys       []passkey.Passkey
	ceremonies map[string]passkey.Ceremony
	added      int
}

func (s *store) List(_ context.Context, u identity.UserID) ([]passkey.Passkey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []passkey.Passkey
	for _, k := range s.keys {
		if k.User == u {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *store) Find(_ context.Context, id []byte) (passkey.Passkey, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if bytes.Equal(k.ID, id) {
			return k, true, nil
		}
	}
	return passkey.Passkey{}, false, nil
}

func (s *store) Add(ctx context.Context, p passkey.Passkey, limit int) error {
	if _, found, _ := s.Find(ctx, p.ID); found {
		return passkey.ErrFailed
	}
	mine, _ := s.List(ctx, p.User)
	if len(mine) >= limit {
		return passkey.ErrTooMany
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, p)
	s.added++
	return nil
}

func (s *store) Used(_ context.Context, id []byte, c webauthn.Credential, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if bytes.Equal(s.keys[i].ID, id) {
			s.keys[i].Credential, s.keys[i].LastUsedAt = c, at
		}
	}
	return nil
}

func (s *store) CloneSuspected(context.Context, identity.UserID, []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clones++
	return nil
}

func (s *store) Rename(_ context.Context, u identity.UserID, id []byte, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].User == u && bytes.Equal(s.keys[i].ID, id) {
			s.keys[i].Name = name
			return nil
		}
	}
	return passkey.ErrNotFound
}

func (s *store) Delete(_ context.Context, u identity.UserID, id []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].User == u && bytes.Equal(s.keys[i].ID, id) {
			s.keys = append(s.keys[:i], s.keys[i+1:]...)
			return nil
		}
	}
	return passkey.ErrNotFound
}

func (s *store) InsertCeremony(_ context.Context, c passkey.Ceremony) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ceremonies[c.IDHash] = c
	return nil
}

func (s *store) TakeCeremony(_ context.Context, h string, now time.Time) (passkey.Ceremony, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ceremonies[h]
	delete(s.ceremonies, h)
	return c, ok && now.Before(c.ExpiresAt), nil
}

func (s *store) DeleteExpiredCeremonies(context.Context, time.Time) error { return nil }

type users struct{}

func (users) Profile(_ context.Context, u identity.UserID) (auth.Profile, bool, error) {
	return auth.Profile{Email: u.String() + "@example.org", DisplayName: "U"}, true, nil
}

func (users) Organizations(context.Context, identity.UserID) ([]auth.Organization, error) {
	return []auth.Organization{{ID: orgA, Name: "Alpha"}}, nil
}

// lockout counts the failures and locks after three.
type lockout struct {
	failures map[identity.UserID]int
}

func (l *lockout) VerifyFactor(_ context.Context, u identity.UserID, check func() (bool, error)) error {
	if l.failures[u] >= 3 {
		return auth.ErrWrongCode
	}
	ok, err := check()
	if err != nil {
		return err
	}
	if !ok {
		l.failures[u]++
		return auth.ErrWrongCode
	}
	l.failures[u] = 0
	return nil
}

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

func (s *memSessions) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}
func (s *memSessions) DeleteForUser(context.Context, identity.UserID) error { return nil }

type env struct {
	svc      *passkey.Service
	store    *store
	lock     *lockout
	sessions *session.Manager
	now      *time.Time
}

func newEnv(t *testing.T) env {
	t.Helper()
	now := t0
	clock := func() time.Time { return now }
	cfg, err := passkey.Config([]string{origin})
	if err != nil {
		t.Fatal(err)
	}
	wa, err := webauthn.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store{ceremonies: map[string]passkey.Ceremony{}}
	lock := &lockout{failures: map[identity.UserID]int{}}
	sessions := session.NewManager(&memSessions{m: map[string]session.Session{}}, clock)
	svc := passkey.NewService(passkey.Deps{
		WebAuthn: wa, Store: st, Users: users{}, Lockout: lock, Sessions: sessions,
		Limiter: ratelimit.New(1000, time.Minute, clock), Now: clock,
	})
	return env{svc: svc, store: st, lock: lock, sessions: sessions, now: &now}
}

func (e env) signedIn(t *testing.T, u identity.UserID) (string, session.Session) {
	t.Helper()
	token, s, err := e.sessions.Create(context.Background(), u, orgA)
	if err != nil {
		t.Fatal(err)
	}
	return token, s
}

// register adds a passkey of a fresh authenticator to u.
func (e env) register(t *testing.T, u identity.UserID, name string) *passkeytest.Authenticator {
	t.Helper()
	ctx := context.Background()
	_, s := e.signedIn(t, u)
	b, err := e.svc.BeginRegistration(ctx, s, name)
	if err != nil {
		t.Fatal(err)
	}
	a := passkeytest.New(origin)
	resp, err := a.Register(b.Options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FinishRegistration(ctx, s, b.CeremonyID, resp); err != nil {
		t.Fatalf("FinishRegistration() = %v", err)
	}
	return a
}

func TestRegistration(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, s := e.signedIn(t, alice)
	b, err := e.svc.BeginRegistration(ctx, s, "  YubiKey  ")
	if err != nil {
		t.Fatal(err)
	}
	var opts struct {
		PublicKey struct {
			RP          struct{ ID string }   `json:"rp"`
			Attestation string                `json:"attestation"`
			User        struct{ Name string } `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(b.Options, &opts); err != nil || opts.PublicKey.RP.ID != "sermon.example.com" ||
		opts.PublicKey.Attestation != "none" || opts.PublicKey.User.Name != alice.String()+"@example.org" {
		t.Errorf("options %s, %v", b.Options, err)
	}
	a := passkeytest.New(origin)
	resp, _ := a.Register(b.Options)
	p, err := e.svc.FinishRegistration(ctx, s, b.CeremonyID, resp)
	if err != nil || p.Name != "YubiKey" || !bytes.Equal(p.ID, a.ID) || p.User != alice || !p.CreatedAt.Equal(t0) {
		t.Fatalf("FinishRegistration() = %+v, %v", p, err)
	}
	// The ceremony works once.
	if _, err := e.svc.FinishRegistration(ctx, s, b.CeremonyID, resp); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("again: %v", err)
	}
	// Another session of the same user can't finish a ceremony.
	b, _ = e.svc.BeginRegistration(ctx, s, "Second")
	resp, _ = passkeytest.New(origin).Register(b.Options)
	_, other := e.signedIn(t, alice)
	if _, err := e.svc.FinishRegistration(ctx, other, b.CeremonyID, resp); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("another session: %v", err)
	}
	// The ceremony of a step-up is no registration, even answered as one.
	token, current := e.signedIn(t, alice)
	sb := begin(t, e, passkey.PurposeStepUp, token)
	var assertion struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	_ = json.Unmarshal(sb.Options, &assertion)
	uid := uuid.UUID(alice)
	forged, _ := json.Marshal(map[string]any{"publicKey": map[string]any{
		"challenge": assertion.PublicKey.Challenge, "rp": map[string]any{"id": "sermon.example.com", "name": "SerMon"},
		"user":             map[string]any{"id": base64.RawURLEncoding.EncodeToString(uid[:]), "name": "a", "displayName": "A"},
		"pubKeyCredParams": []any{map[string]any{"type": "public-key", "alg": -7}},
	}})
	resp, _ = passkeytest.New(origin).Register(forged)
	if _, err := e.svc.FinishRegistration(ctx, current, sb.CeremonyID, resp); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("a step-up ceremony as a registration: %v", err)
	}
	if n, _ := e.svc.Count(ctx, alice); n != 1 {
		t.Errorf("Count() = %d", n)
	}
}

func TestRegistration_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(e env, a *passkeytest.Authenticator, s *session.Session, id *string)
	}{
		"wrong origin": {func(_ env, a *passkeytest.Authenticator, _ *session.Session, _ *string) {
			a.Origin = "https://evil.example"
		}},
		"another rp id":  {func(_ env, a *passkeytest.Authenticator, _ *session.Session, _ *string) { a.RPID = "evil.example" }},
		"another user":   {func(e env, _ *passkeytest.Authenticator, s *session.Session, _ *string) { _, *s = e.signedIn(t, bob) }},
		"other ceremony": {func(_ env, _ *passkeytest.Authenticator, _ *session.Session, id *string) { *id = "forged" }},
		"expired": {func(e env, _ *passkeytest.Authenticator, s *session.Session, _ *string) {
			*e.now = e.now.Add(passkey.CeremonyLifetime + time.Second)
			s.StepUpAt = *e.now
		}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			_, s := e.signedIn(t, alice)
			b, err := e.svc.BeginRegistration(ctx, s, "Key")
			if err != nil {
				t.Fatal(err)
			}
			a := passkeytest.New(origin)
			id := b.CeremonyID
			resp, _ := a.Register(b.Options)
			tc.change(e, a, &s, &id)
			if name == "wrong origin" || name == "another rp id" {
				resp = registerAs(t, a, b.Options)
			}
			if _, err := e.svc.FinishRegistration(ctx, s, id, resp); !errors.Is(err, passkey.ErrFailed) {
				t.Errorf("FinishRegistration() = %v", err)
			}
			if e.store.added != 0 {
				t.Error("stored anyway")
			}
		})
	}
}

// registerAs answers again after the authenticator was changed (origin, RP ID kept from a).
func registerAs(t *testing.T, a *passkeytest.Authenticator, options []byte) []byte {
	t.Helper()
	rp := a.RPID
	var o map[string]map[string]any
	_ = json.Unmarshal(options, &o)
	o["publicKey"]["rp"] = map[string]any{"id": rp, "name": "SerMon"}
	forged, _ := json.Marshal(o)
	resp, err := a.Register(forged)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRegistration_StepUpNameAndLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, s := e.signedIn(t, alice)
	*e.now = e.now.Add(20 * time.Minute)
	if _, err := e.svc.BeginRegistration(ctx, s, "Key"); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("old step-up: %v", err)
	}
	_, s = e.signedIn(t, alice)
	b, _ := e.svc.BeginRegistration(ctx, s, "Key")
	a := passkeytest.New(origin)
	resp, _ := a.Register(b.Options)
	old := s
	old.StepUpAt = t0
	*e.now = e.now.Add(20 * time.Minute)
	if _, err := e.svc.FinishRegistration(ctx, old, b.CeremonyID, resp); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("finish with an old step-up: %v", err)
	}
	_, s = e.signedIn(t, alice)
	for _, name := range []string{"", "   ", "a\nb", strings.Repeat("x", 65)} {
		if _, err := e.svc.BeginRegistration(ctx, s, name); !errors.Is(err, passkey.ErrInvalidName) {
			t.Errorf("name %q: %v", name, err)
		}
	}
	e2 := newEnv(t)
	for range passkey.MaxPerUser {
		e2.register(t, alice, "Key")
	}
	_, s = e2.signedIn(t, alice)
	if _, err := e2.svc.BeginRegistration(ctx, s, "One more"); !errors.Is(err, passkey.ErrTooMany) {
		t.Errorf("beyond the limit: %v", err)
	}
}

func begin(t *testing.T, e env, purpose passkey.Purpose, token string) passkey.Begun {
	t.Helper()
	b, err := e.svc.Begin(context.Background(), purpose, token, "192.0.2.1")
	if err != nil {
		t.Fatalf("Begin(%d) = %v", purpose, err)
	}
	return b
}

func TestLogin_WithoutPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.register(t, alice, "Phone")
	b := begin(t, e, passkey.PurposeLogin, "")
	if b.Binding == "" {
		t.Fatal("no binding")
	}
	var opts struct {
		PublicKey struct {
			UserVerification string `json:"userVerification"`
			Allow            []any  `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	_ = json.Unmarshal(b.Options, &opts)
	if opts.PublicKey.UserVerification != "required" || len(opts.PublicKey.Allow) != 0 {
		t.Errorf("options %s", b.Options)
	}
	resp, _ := a.Assert(b.Options)
	res, err := e.svc.Finish(ctx, passkey.PurposeLogin, b.CeremonyID, b.Binding, "", resp, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Session.User != alice || res.Session.Org != orgA || !res.Session.ExternalMFA || len(res.Organizations) != 1 {
		t.Errorf("result %+v", res)
	}
	if keys, _ := e.svc.List(ctx, alice); !keys[0].LastUsedAt.Equal(t0) || keys[0].Credential.Authenticator.SignCount != 2 {
		t.Errorf("use not stored: %+v", keys[0])
	}
	if _, err := e.svc.Finish(ctx, passkey.PurposeLogin, b.CeremonyID, b.Binding, "", resp, "192.0.2.1"); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("the ceremony again: %v", err)
	}
}

func TestLogin_Refusals(t *testing.T) {
	for name, tc := range map[string]func(e env, a *passkeytest.Authenticator, b *passkey.Begun){
		"another browser":      func(_ env, _ *passkeytest.Authenticator, b *passkey.Begun) { b.Binding = "other" },
		"without verification": func(_ env, a *passkeytest.Authenticator, _ *passkey.Begun) { a.Verify = false },
		"wrong origin": func(_ env, a *passkeytest.Authenticator, _ *passkey.Begun) {
			a.Origin = "https://sermon.example.com.evil.example"
		},
		"another rp id":     func(_ env, a *passkeytest.Authenticator, _ *passkey.Begun) { a.RPID = "evil.example" },
		"counter backwards": func(_ env, a *passkeytest.Authenticator, _ *passkey.Begun) { a.Count = 1 },
		"unknown passkey": func(_ env, a *passkeytest.Authenticator, _ *passkey.Begun) {
			a.ID = []byte("an unknown credential id 123456")
		},
		"another user's handle": func(_ env, a *passkeytest.Authenticator, _ *passkey.Begun) { u := uuid.UUID(bob); a.User = u[:] },
		"expired": func(e env, _ *passkeytest.Authenticator, _ *passkey.Begun) {
			*e.now = e.now.Add(passkey.CeremonyLifetime)
		},
		"a registration ceremony": func(_ env, _ *passkeytest.Authenticator, b *passkey.Begun) { b.CeremonyID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a := e.register(t, alice, "Phone")
			b := begin(t, e, passkey.PurposeLogin, "")
			tc(e, a, &b)
			opts := b.Options
			if name == "another rp id" {
				var o map[string]map[string]any
				_ = json.Unmarshal(opts, &o)
				o["publicKey"]["rpId"] = "evil.example"
				opts, _ = json.Marshal(o)
			}
			resp, err := a.Assert(opts)
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.svc.Finish(context.Background(), passkey.PurposeLogin, b.CeremonyID, b.Binding, "", resp, "192.0.2.1")
			if !errors.Is(err, passkey.ErrFailed) || res.Token != "" {
				t.Errorf("Finish() = %+v, %v", res, err)
			}
		})
	}
}

// Synced passkeys always report 0: that is no clone.
func TestLogin_SyncedPasskeysCountZero(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, s := e.signedIn(t, alice)
	b, _ := e.svc.BeginRegistration(ctx, s, "Synced")
	a := passkeytest.New(origin)
	a.Count, a.Synced = 0, true
	resp, _ := a.Register(b.Options)
	if _, err := e.svc.FinishRegistration(ctx, s, b.CeremonyID, resp); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		lb := begin(t, e, passkey.PurposeLogin, "")
		resp, _ := a.Assert(lb.Options)
		if _, err := e.svc.Finish(ctx, passkey.PurposeLogin, lb.CeremonyID, lb.Binding, "", resp, "192.0.2.1"); err != nil {
			t.Fatalf("a synced passkey: %v", err)
		}
	}
}

func TestLogin_FailuresCountTowardsTheLockout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.register(t, alice, "Phone")
	a.Verify = false
	for range 3 {
		b := begin(t, e, passkey.PurposeLogin, "")
		resp, _ := a.Assert(b.Options)
		_, _ = e.svc.Finish(ctx, passkey.PurposeLogin, b.CeremonyID, b.Binding, "", resp, "192.0.2.1")
	}
	if e.lock.failures[alice] != 3 {
		t.Fatalf("%d failures counted", e.lock.failures[alice])
	}
	a.Verify = true
	b := begin(t, e, passkey.PurposeLogin, "")
	resp, _ := a.Assert(b.Options)
	if _, err := e.svc.Finish(ctx, passkey.PurposeLogin, b.CeremonyID, b.Binding, "", resp, "192.0.2.1"); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("a locked account: %v", err)
	}
}

func TestSecondFactorAndStepUp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.register(t, alice, "Key")
	a.Verify = false // presence is enough here
	pending, _, err := e.sessions.CreatePending(ctx, alice, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Begin(ctx, passkey.PurposeSecondFactor, "nope", "192.0.2.1"); !errors.Is(err, passkey.ErrNotSignedIn) {
		t.Errorf("without a waiting session: %v", err)
	}
	b := begin(t, e, passkey.PurposeSecondFactor, pending)
	var opts struct {
		PublicKey struct {
			Allow []struct{ ID string } `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	_ = json.Unmarshal(b.Options, &opts)
	if len(opts.PublicKey.Allow) != 1 {
		t.Errorf("allowed credentials %s", b.Options)
	}
	resp, _ := a.Assert(b.Options)
	// Another session can't finish it.
	other, _, _ := e.sessions.CreatePending(ctx, alice, orgA)
	if _, err := e.svc.Finish(ctx, passkey.PurposeSecondFactor, b.CeremonyID, "", other, resp, "192.0.2.1"); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("another session: %v", err)
	}
	b = begin(t, e, passkey.PurposeSecondFactor, pending)
	resp, _ = a.Assert(b.Options)
	res, err := e.svc.Finish(ctx, passkey.PurposeSecondFactor, b.CeremonyID, "", pending, resp, "192.0.2.1")
	if err != nil || res.Token == "" || res.Session.SecondFactorPending || res.Session.User != alice {
		t.Fatalf("second factor = %+v, %v", res, err)
	}
	// The step-up of the full session.
	*e.now = e.now.Add(20 * time.Minute)
	b = begin(t, e, passkey.PurposeStepUp, res.Token)
	resp, _ = a.Assert(b.Options)
	// A ceremony of another purpose doesn't count.
	if _, err := e.svc.Finish(ctx, passkey.PurposeSecondFactor, b.CeremonyID, "", res.Token, resp, "192.0.2.1"); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("another purpose: %v", err)
	}
	b = begin(t, e, passkey.PurposeStepUp, res.Token)
	resp, _ = a.Assert(b.Options)
	stepped, err := e.svc.Finish(ctx, passkey.PurposeStepUp, b.CeremonyID, "", res.Token, resp, "192.0.2.1")
	if err != nil || !stepped.Session.StepUpAt.Equal(*e.now) || stepped.Token != "" {
		t.Errorf("step-up = %+v, %v", stepped, err)
	}
	// Bob's passkey is no step-up for Alice.
	bobKey := e.register(t, bob, "Bob")
	b = begin(t, e, passkey.PurposeStepUp, res.Token)
	resp, _ = bobKey.Assert(b.Options)
	if _, err := e.svc.Finish(ctx, passkey.PurposeStepUp, b.CeremonyID, "", res.Token, resp, "192.0.2.1"); !errors.Is(err, passkey.ErrFailed) {
		t.Errorf("another user's passkey: %v", err)
	}
	// Without a passkey there's nothing to begin.
	token, _ := e.signedIn(t, identity.UserID(uuid.New()))
	if _, err := e.svc.Begin(ctx, passkey.PurposeStepUp, token, "192.0.2.1"); !errors.Is(err, passkey.ErrNotFound) {
		t.Errorf("no passkey: %v", err)
	}
}

func TestRenameAndDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.register(t, alice, "Key")
	if err := e.svc.Rename(ctx, alice, a.ID, " Laptop "); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Rename(ctx, bob, a.ID, "Mine"); !errors.Is(err, passkey.ErrNotFound) {
		t.Errorf("rename another's: %v", err)
	}
	if err := e.svc.Rename(ctx, alice, a.ID, ""); !errors.Is(err, passkey.ErrInvalidName) {
		t.Errorf("empty name: %v", err)
	}
	keys, _ := e.svc.List(ctx, alice)
	if keys[0].Name != "Laptop" {
		t.Errorf("name %q", keys[0].Name)
	}
	_, s := e.signedIn(t, alice)
	_, bs := e.signedIn(t, bob)
	if err := e.svc.Delete(ctx, bs, a.ID); !errors.Is(err, passkey.ErrNotFound) {
		t.Errorf("delete another's: %v", err)
	}
	*e.now = e.now.Add(20 * time.Minute)
	if err := e.svc.Delete(ctx, s, a.ID); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("old step-up: %v", err)
	}
	_, s = e.signedIn(t, alice)
	if err := e.svc.Delete(ctx, s, a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.svc.Count(ctx, alice); n != 0 {
		t.Errorf("%d left", n)
	}
}

func TestConfig_RelyingPartyFromTheOrigins(t *testing.T) {
	cfg, err := passkey.Config([]string{"https://sermon.example.com", "https://ops.sermon.example.com:8443", "https://other.example", "http://localhost:5173"})
	if err != nil || cfg.RPID != "sermon.example.com" || len(cfg.RPOrigins) != 2 {
		t.Errorf("Config() = %+v, %v", cfg, err)
	}
	if cfg, err := passkey.Config([]string{"https://localhost:18443", "https://sermon.test"}); err != nil || cfg.RPID != "localhost" || len(cfg.RPOrigins) != 1 {
		t.Errorf("e2e origins: %+v, %v", cfg, err)
	}
	for _, bad := range [][]string{nil, {"https://[::1]:8443"}, {"nohost"}} {
		if _, err := passkey.Config(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	var off *passkey.Service
	if off.Available() || passkey.NewService(passkey.Deps{}).Available() {
		t.Error("available without a relying party")
	}
}

// M3-28: a counter going back is recorded, not only refused.
func TestLogin_ACloneIsRecorded(t *testing.T) {
	e := newEnv(t)
	a := e.register(t, alice, "Phone")
	a.Count = 1
	b := begin(t, e, passkey.PurposeLogin, "")
	resp, _ := a.Assert(b.Options)
	if _, err := e.svc.Finish(context.Background(), passkey.PurposeLogin, b.CeremonyID, b.Binding, "", resp, "192.0.2.1"); !errors.Is(err, passkey.ErrFailed) {
		t.Fatalf("Finish() = %v", err)
	}
	if e.store.clones != 1 {
		t.Errorf("%d clones recorded", e.store.clones)
	}
}
