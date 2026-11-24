package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

var (
	t0    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	alice = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	fast  = password.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32}
)

// Test fixture (the well-known XKCD passphrase), not a secret.
const alicePassword = "correct horse battery staple" // betterleaks:allow

type fakeCredentials struct {
	mu sync.Mutex
	// barrier > 0 makes the first Get calls wait until that many have arrived (or a timeout), so
	// that parallel attempts read the same state – the race of a real database.
	barrier, arrived int
	released         chan struct{}

	users                map[string]identity.UserID
	creds                map[identity.UserID]auth.Credentials
	orgs                 map[identity.UserID][]auth.Organization
	writes, reservations int
}

func (f *fakeCredentials) FindUserByEmail(_ context.Context, email string) (identity.UserID, bool, error) {
	id, ok := f.users[strings.ToLower(email)]
	return id, ok, nil
}

func (f *fakeCredentials) Get(_ context.Context, user identity.UserID) (auth.Credentials, bool, error) {
	f.mu.Lock()
	c, ok := f.creds[user]
	if f.barrier > 0 && f.released == nil {
		f.released = make(chan struct{})
		go func(ch chan struct{}) { time.Sleep(300 * time.Millisecond); f.release(ch) }(f.released)
	}
	wait := f.barrier > 0 && f.arrived < f.barrier
	if wait {
		f.arrived++
		if f.arrived == f.barrier {
			f.mu.Unlock()
			f.release(f.released)
			return c, ok, nil
		}
	}
	ch := f.released
	f.mu.Unlock()
	if wait {
		<-ch
	}
	return c, ok, nil
}

func (f *fakeCredentials) release(ch chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// ReserveAttempt behaves like the SQL compare-and-swap.
func (f *fakeCredentials) ReserveAttempt(_ context.Context, user identity.UserID, seen int32, lockedUntil *time.Time, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.creds[user]
	if c.FailedAttempts != seen || (c.LockedUntil != nil && now.Before(*c.LockedUntil)) {
		return false, nil
	}
	f.writes++
	f.reservations++
	c.FailedAttempts++
	c.LockedUntil = lockedUntil
	f.creds[user] = c
	return true, nil
}

func (f *fakeCredentials) ReleaseAttempt(_ context.Context, user identity.UserID, lockAfter int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	c := f.creds[user]
	c.FailedAttempts = max(c.FailedAttempts-1, 0)
	if c.FailedAttempts < lockAfter {
		c.LockedUntil = nil
	}
	f.creds[user] = c
	return nil
}

func (f *fakeCredentials) ResetFailedLogins(_ context.Context, user identity.UserID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	c := f.creds[user]
	c.FailedAttempts, c.LockedUntil = 0, nil
	f.creds[user] = c
	return nil
}

func (f *fakeCredentials) UpdatePasswordHash(_ context.Context, user identity.UserID, hash string) error {
	f.writes++
	c := f.creds[user]
	c.PasswordHash = hash
	f.creds[user] = c
	return nil
}

func (f *fakeCredentials) Organizations(_ context.Context, user identity.UserID) ([]auth.Organization, error) {
	return f.orgs[user], nil
}

func (f *fakeCredentials) Profile(_ context.Context, user identity.UserID) (auth.Profile, bool, error) {
	if user != alice {
		return auth.Profile{}, false, nil
	}
	return auth.Profile{Email: "alice@example.org", DisplayName: "Alice"}, true, nil
}

type memSessions struct{ m map[string]session.Session }

func (s *memSessions) Insert(_ context.Context, se session.Session) error {
	s.m[se.ID] = se
	return nil
}

func (s *memSessions) Get(_ context.Context, id string) (session.Session, bool, error) {
	se, ok := s.m[id]
	return se, ok, nil
}
func (s *memSessions) Touch(context.Context, string, time.Time) error { return nil }
func (s *memSessions) SetStepUp(_ context.Context, id string, at time.Time) error {
	se := s.m[id]
	se.StepUpAt = at
	s.m[id] = se
	return nil
}

func (s *memSessions) Delete(_ context.Context, id string) error                    { delete(s.m, id); return nil }
func (s *memSessions) DeleteForUser(context.Context, identity.UserID, string) error { return nil }

type env struct {
	svc   *auth.Service
	creds *fakeCredentials
	now   *time.Time
	hash  *password.Hasher
}

func newEnv(t *testing.T, hashParams password.Params) env {
	t.Helper()
	h, err := password.NewHasher(hashParams)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := h.Hash(alicePassword)
	creds := &fakeCredentials{
		users: map[string]identity.UserID{"alice@example.org": alice},
		creds: map[identity.UserID]auth.Credentials{alice: {User: alice, PasswordHash: encoded}},
		orgs:  map[identity.UserID][]auth.Organization{alice: {{ID: orgA, Name: "Org A"}, {ID: orgB, Name: "Org B"}}},
	}
	now := t0
	clock := func() time.Time { return now }
	current, _ := password.NewHasher(fast)
	svc := auth.NewService(auth.Deps{
		Credentials: creds,
		Sessions:    session.NewManager(&memSessions{m: map[string]session.Session{}}, clock),
		Hasher:      current,
		IPLimiter:   ratelimit.New(20, time.Minute, clock),
		Now:         clock,
	})
	return env{svc: svc, creds: creds, now: &now, hash: current}
}

func TestLogin_SucceedsAndStartsASessionInTheFirstOrganization(t *testing.T) {
	e := newEnv(t, fast)

	res, err := e.svc.Login(context.Background(), " Alice@Example.org ", alicePassword, "192.0.2.10")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if res.Token == "" || res.Session.User != alice || res.Session.Org != orgA || len(res.Organizations) != 2 {
		t.Errorf("result = %+v", res)
	}
}

func TestLogin_WrongPasswordAndUnknownUserLookTheSame(t *testing.T) {
	e := newEnv(t, fast)

	_, errWrong := e.svc.Login(context.Background(), "alice@example.org", "wrong password here", "192.0.2.10")
	_, errUnknown := e.svc.Login(context.Background(), "mallory@example.org", alicePassword, "192.0.2.10")

	if !errors.Is(errWrong, auth.ErrInvalidCredentials) || !errors.Is(errUnknown, auth.ErrInvalidCredentials) {
		t.Fatalf("errors = %v / %v, want ErrInvalidCredentials", errWrong, errUnknown)
	}
	if errWrong.Error() != errUnknown.Error() {
		t.Errorf("messages differ: %q vs %q", errWrong, errUnknown)
	}
}

func TestLogin_LocksTheAccountProgressively(t *testing.T) {
	e := newEnv(t, fast)
	ctx := context.Background()
	wantLocks := map[int]time.Duration{5: time.Minute, 6: 2 * time.Minute, 7: 4 * time.Minute, 8: 8 * time.Minute, 9: 15 * time.Minute, 12: 15 * time.Minute}

	for attempt := 1; attempt <= 12; attempt++ {
		_, _ = e.svc.Login(ctx, "alice@example.org", "wrong password here", "192.0.2.10")
		c := e.creds.creds[alice]
		want, locks := wantLocks[attempt]
		if attempt < 5 && c.LockedUntil != nil {
			t.Fatalf("attempt %d: locked too early", attempt)
		}
		if locks && (c.LockedUntil == nil || !c.LockedUntil.Equal(e.now.Add(want))) {
			t.Errorf("attempt %d: locked until %v, want now+%s", attempt, c.LockedUntil, want)
		}
		*e.now = e.now.Add(16 * time.Minute) // let the lock expire before the next attempt
	}
}

func TestLogin_LockedAccountRejectsEvenTheCorrectPassword(t *testing.T) {
	e := newEnv(t, fast)
	ctx := context.Background()
	for range 5 {
		_, _ = e.svc.Login(ctx, "alice@example.org", "wrong password here", "192.0.2.10")
	}

	if _, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("locked account: error = %v, want ErrInvalidCredentials", err)
	}
	*e.now = e.now.Add(time.Minute)
	if _, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10"); err != nil {
		t.Errorf("after the lock expired: error = %v", err)
	}
	if c := e.creds.creds[alice]; c.FailedAttempts != 0 || c.LockedUntil != nil {
		t.Errorf("successful login must reset failures: %+v", c)
	}
}

func TestLogin_RateLimitsPerClientIP(t *testing.T) {
	e := newEnv(t, fast)
	ctx := context.Background()
	for range 20 {
		_, _ = e.svc.Login(ctx, "mallory@example.org", "guess guess guess", "198.51.100.7")
	}

	if _, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "198.51.100.7"); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("error = %v, want ErrTooManyAttempts", err)
	}
	if _, err := e.svc.Login(ctx, "alice@example.org", alicePassword, "192.0.2.10"); err != nil {
		t.Errorf("other client IPs are not affected: %v", err)
	}
}

func TestLogin_RehashesOutdatedHashes(t *testing.T) {
	e := newEnv(t, password.Params{Memory: 16 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32})

	if _, err := e.svc.Login(context.Background(), "alice@example.org", alicePassword, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	if e.hash.NeedsRehash(e.creds.creds[alice].PasswordHash) {
		t.Error("the hash must be upgraded to the current parameters after a successful login")
	}
}

func TestLogin_RejectsOversizedInputBeforeAnyWork(t *testing.T) {
	e := newEnv(t, fast)
	ctx := context.Background()
	for name, in := range map[string][2]string{
		"long e-mail":    {strings.Repeat("a", 321) + "@x.org", alicePassword},
		"long password":  {"alice@example.org", strings.Repeat("a", password.MaxLength+1)},
		"empty e-mail":   {"", alicePassword},
		"empty password": {"alice@example.org", ""},
	} {
		if _, err := e.svc.Login(ctx, in[0], in[1], "192.0.2.10"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("%s: error = %v, want ErrInvalidCredentials", name, err)
		}
	}
	if e.creds.writes != 0 {
		t.Error("invalid input must not touch the account")
	}
}

func TestLogin_UserWithoutPasswordCannotLogIn(t *testing.T) {
	e := newEnv(t, fast)
	delete(e.creds.creds, alice) // e.g. an SSO-only account

	if _, err := e.svc.Login(context.Background(), "alice@example.org", alicePassword, "192.0.2.10"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLogin_UserWithoutOrganizationGetsAnInstanceSession(t *testing.T) {
	e := newEnv(t, fast)
	e.creds.orgs[alice] = nil

	res, err := e.svc.Login(context.Background(), "alice@example.org", alicePassword, "192.0.2.10")
	if err != nil || res.Session.Org != (tenant.OrgID{}) {
		t.Errorf("Login() = %+v, %v; want a session without organization", res.Session, err)
	}
}

type failingCredentials struct {
	*fakeCredentials
	fail string
}

var errDB = errors.New("database unavailable")

func (f *failingCredentials) FindUserByEmail(ctx context.Context, e string) (identity.UserID, bool, error) {
	if f.fail == "find" {
		return identity.UserID{}, false, errDB
	}
	return f.fakeCredentials.FindUserByEmail(ctx, e)
}

func (f *failingCredentials) Get(ctx context.Context, u identity.UserID) (auth.Credentials, bool, error) {
	if f.fail == "get" {
		return auth.Credentials{}, false, errDB
	}
	return f.fakeCredentials.Get(ctx, u)
}

func (f *failingCredentials) ReserveAttempt(ctx context.Context, u identity.UserID, seen int32, l *time.Time, now time.Time) (bool, error) {
	if f.fail == "record" {
		return false, errDB
	}
	return f.fakeCredentials.ReserveAttempt(ctx, u, seen, l, now)
}

func (f *failingCredentials) ResetFailedLogins(ctx context.Context, u identity.UserID) error {
	if f.fail == "reset" {
		return errDB
	}
	return f.fakeCredentials.ResetFailedLogins(ctx, u)
}

func (f *failingCredentials) Organizations(ctx context.Context, u identity.UserID) ([]auth.Organization, error) {
	if f.fail == "orgs" {
		return nil, errDB
	}
	return f.fakeCredentials.Organizations(ctx, u)
}

func TestLogin_RepositoryErrorsNeverLogIn(t *testing.T) {
	for _, op := range []string{"find", "get", "record", "reset", "orgs"} {
		t.Run(op, func(t *testing.T) {
			e := newEnv(t, fast)
			pw := alicePassword
			if op == "record" {
				pw = "wrong password here"
			}
			if op == "reset" {
				c := e.creds.creds[alice]
				c.FailedAttempts = 1
				e.creds.creds[alice] = c
			}
			now := *e.now
			svc := auth.NewService(auth.Deps{
				Credentials: &failingCredentials{fakeCredentials: e.creds, fail: op},
				Sessions:    session.NewManager(&memSessions{m: map[string]session.Session{}}, func() time.Time { return now }),
				Hasher:      e.hash,
				IPLimiter:   ratelimit.New(20, time.Minute, func() time.Time { return now }),
				Now:         func() time.Time { return now },
			})

			res, err := svc.Login(context.Background(), "alice@example.org", pw, "192.0.2.10")
			if !errors.Is(err, errDB) || res.Token != "" {
				t.Errorf("Login() = %+v, %v; want errDB and no token", res, err)
			}
		})
	}
}

// Parallel requests must not read the same unlocked state and all get a full check: the account
// locks after five attempts however they arrive (KONZEPT §7).
func TestLogin_ParallelAttemptsCannotOutrunTheLockout(t *testing.T) {
	e := newEnv(t, fast)
	e.creds.barrier = 12
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, _ = e.svc.Login(context.Background(), "alice@example.org", "wrong password here", "192.0.2.10")
		})
	}
	wg.Wait()

	c := e.creds.creds[alice]
	if c.LockedUntil == nil || c.FailedAttempts != 5 {
		t.Fatalf("after 12 parallel failures: %+v, want locked after exactly 5", c)
	}
	if e.creds.reservations != 5 {
		t.Errorf("%d attempts were checked, want 5", e.creds.reservations)
	}
	if _, err := e.svc.Login(context.Background(), "alice@example.org", alicePassword, "192.0.2.10"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("the right password during the lock: %v", err)
	}
}
