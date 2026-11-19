package session_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	t0    = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	alice = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	bob   = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
)

type fakeStore struct {
	byHash  map[string]session.Session
	touches int
}

func newStore() *fakeStore { return &fakeStore{byHash: map[string]session.Session{}} }

func (f *fakeStore) Insert(_ context.Context, s session.Session) error {
	f.byHash[s.ID] = s
	return nil
}

func (f *fakeStore) Get(_ context.Context, id string) (session.Session, bool, error) {
	s, ok := f.byHash[id]
	return s, ok, nil
}

func (f *fakeStore) Touch(_ context.Context, id string, at time.Time) error {
	f.touches++
	s := f.byHash[id]
	s.LastSeenAt = at
	f.byHash[id] = s
	return nil
}

func (f *fakeStore) SetStepUp(_ context.Context, id string, at time.Time) error {
	s := f.byHash[id]
	s.StepUpAt = at
	f.byHash[id] = s
	return nil
}

func (f *fakeStore) Delete(_ context.Context, id string) error {
	delete(f.byHash, id)
	return nil
}

func (f *fakeStore) DeleteForUser(_ context.Context, user identity.UserID) error {
	for id, s := range f.byHash {
		if s.User == user {
			delete(f.byHash, id)
		}
	}
	return nil
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func manager(t *testing.T) (*session.Manager, *fakeStore, *clock) {
	t.Helper()
	store, c := newStore(), &clock{now: t0}
	return session.NewManager(store, c.Now), store, c
}

func TestCreate_ReturnsARandomTokenAndStoresOnlyItsHash(t *testing.T) {
	m, store, _ := manager(t)

	token, s, err := m.Create(context.Background(), alice, orgA)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	other, _, _ := m.Create(context.Background(), alice, orgA)

	if len(token) < 43 || token == other {
		t.Errorf("tokens must be long and unique: %q, %q", token, other)
	}
	if _, ok := store.byHash[token]; ok || strings.Contains(s.ID, token) {
		t.Error("the store must not contain the token itself")
	}
	if s.User != alice || s.Org != orgA || !s.CreatedAt.Equal(t0) || !s.StepUpAt.Equal(t0) {
		t.Errorf("session = %+v", s)
	}
}

func TestValidate_AcceptsAFreshSession(t *testing.T) {
	m, _, _ := manager(t)
	token, _, _ := m.Create(context.Background(), alice, orgA)

	s, err := m.Validate(context.Background(), token)
	if err != nil || s.User != alice {
		t.Errorf("Validate() = %+v, %v", s, err)
	}
}

func TestValidate_IdleTimeout(t *testing.T) {
	m, _, c := manager(t)
	token, _, _ := m.Create(context.Background(), alice, orgA)

	c.advance(session.IdleTimeout - time.Second)
	if _, err := m.Validate(context.Background(), token); err != nil {
		t.Fatalf("activity just before the idle timeout must keep the session: %v", err)
	}
	c.advance(session.IdleTimeout - time.Second) // still active: last seen was renewed
	if _, err := m.Validate(context.Background(), token); err != nil {
		t.Fatalf("session must stay alive while it is used: %v", err)
	}
	c.advance(session.IdleTimeout)
	if _, err := m.Validate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("idle session: error = %v, want ErrInvalid", err)
	}
}

func TestValidate_AbsoluteTimeoutEvenWhenActive(t *testing.T) {
	m, _, c := manager(t)
	token, _, _ := m.Create(context.Background(), alice, orgA)

	for elapsed := time.Duration(0); elapsed < session.AbsoluteTimeout-10*time.Minute; elapsed += 10 * time.Minute {
		c.advance(10 * time.Minute)
		if _, err := m.Validate(context.Background(), token); err != nil {
			t.Fatalf("active session expired early after %s: %v", elapsed, err)
		}
	}
	c.now = t0.Add(session.AbsoluteTimeout)
	if _, err := m.Validate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("after %s: error = %v, want ErrInvalid", session.AbsoluteTimeout, err)
	}
}

func TestValidate_ExpiredSessionsAreDeleted(t *testing.T) {
	m, store, c := manager(t)
	token, _, _ := m.Create(context.Background(), alice, orgA)

	c.advance(session.IdleTimeout + time.Second)
	_, _ = m.Validate(context.Background(), token)

	if len(store.byHash) != 0 {
		t.Error("an expired session must be removed from the store")
	}
}

func TestValidate_TouchesAtMostOncePerMinute(t *testing.T) {
	m, store, c := manager(t)
	token, _, _ := m.Create(context.Background(), alice, orgA)

	for range 10 {
		c.advance(5 * time.Second)
		_, _ = m.Validate(context.Background(), token)
	}
	if store.touches > 1 {
		t.Errorf("%d store writes for 50 s of activity, want at most 1", store.touches)
	}
}

func TestValidate_RejectsUnknownAndMalformedTokens(t *testing.T) {
	m, _, _ := manager(t)
	for _, token := range []string{"", "x", strings.Repeat("a", 43), strings.Repeat("a", 10000)} {
		if _, err := m.Validate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
			t.Errorf("Validate(%.20q) error = %v, want ErrInvalid", token, err)
		}
	}
}

func TestRevoke_EndsTheSession(t *testing.T) {
	m, _, _ := manager(t)
	token, _, _ := m.Create(context.Background(), alice, orgA)

	if err := m.Revoke(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Validate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("revoked session: error = %v, want ErrInvalid", err)
	}
}

func TestRevokeAllForUser_EndsOnlyThatUsersSessions(t *testing.T) {
	m, _, _ := manager(t)
	a1, _, _ := m.Create(context.Background(), alice, orgA)
	a2, _, _ := m.Create(context.Background(), alice, orgA)
	b1, _, _ := m.Create(context.Background(), bob, orgA)

	if err := m.RevokeAllForUser(context.Background(), alice); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{a1, a2} {
		if _, err := m.Validate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
			t.Error("alice's sessions must be revoked")
		}
	}
	if _, err := m.Validate(context.Background(), b1); err != nil {
		t.Errorf("bob's session must survive: %v", err)
	}
}

func TestStepUp(t *testing.T) {
	m, _, c := manager(t)
	token, _, _ := m.Create(context.Background(), alice, orgA)
	const maxAge = 5 * time.Minute

	s, _ := m.Validate(context.Background(), token)
	if !s.SteppedUpWithin(maxAge, c.Now()) {
		t.Error("a fresh login counts as recent authentication")
	}
	c.advance(maxAge + time.Second)
	s, _ = m.Validate(context.Background(), token)
	if s.SteppedUpWithin(maxAge, c.Now()) {
		t.Error("authentication older than maxAge must require a step-up")
	}
	if err := m.StepUp(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Validate(context.Background(), token)
	if !s.SteppedUpWithin(maxAge, c.Now()) {
		t.Error("after StepUp the session counts as recently authenticated")
	}
}

func TestStepUp_RejectsInvalidSessions(t *testing.T) {
	m, _, _ := manager(t)
	if err := m.StepUp(context.Background(), "unknown"); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("StepUp(unknown) error = %v, want ErrInvalid", err)
	}
}

type brokenStore struct {
	*fakeStore
	fail string
}

var errDB = errors.New("database unavailable")

func (b *brokenStore) Insert(ctx context.Context, s session.Session) error {
	if b.fail == "insert" {
		return errDB
	}
	return b.fakeStore.Insert(ctx, s)
}

func (b *brokenStore) Get(ctx context.Context, id string) (session.Session, bool, error) {
	if b.fail == "get" {
		return session.Session{}, false, errDB
	}
	return b.fakeStore.Get(ctx, id)
}

func (b *brokenStore) Touch(ctx context.Context, id string, at time.Time) error {
	if b.fail == "touch" {
		return errDB
	}
	return b.fakeStore.Touch(ctx, id, at)
}

func (b *brokenStore) SetStepUp(ctx context.Context, id string, at time.Time) error {
	if b.fail == "stepup" {
		return errDB
	}
	return b.fakeStore.SetStepUp(ctx, id, at)
}

func (b *brokenStore) Delete(ctx context.Context, id string) error {
	if b.fail == "delete" {
		return errDB
	}
	return b.fakeStore.Delete(ctx, id)
}

func (b *brokenStore) DeleteForUser(ctx context.Context, u identity.UserID) error {
	if b.fail == "deleteuser" {
		return errDB
	}
	return b.fakeStore.DeleteForUser(ctx, u)
}

func TestManager_StoreErrorsNeverYieldAValidSession(t *testing.T) {
	c := &clock{now: t0}
	store := &brokenStore{fakeStore: newStore()}
	m := session.NewManager(store, c.Now)
	token, _, _ := m.Create(context.Background(), alice, orgA)
	ctx := context.Background()

	store.fail = "insert"
	if _, _, err := m.Create(ctx, alice, orgA); !errors.Is(err, errDB) {
		t.Errorf("Create: %v", err)
	}
	store.fail = "get"
	if _, err := m.Validate(ctx, token); !errors.Is(err, errDB) {
		t.Errorf("Validate/get: %v", err)
	}
	store.fail = "touch"
	c.advance(2 * time.Minute)
	if _, err := m.Validate(ctx, token); !errors.Is(err, errDB) {
		t.Errorf("Validate/touch: %v", err)
	}
	store.fail = "stepup"
	if err := m.StepUp(ctx, token); !errors.Is(err, errDB) {
		t.Errorf("StepUp: %v", err)
	}
	store.fail = "delete"
	if err := m.Revoke(ctx, token); !errors.Is(err, errDB) {
		t.Errorf("Revoke: %v", err)
	}
	c.advance(session.IdleTimeout)
	if _, err := m.Validate(ctx, token); !errors.Is(err, errDB) {
		t.Errorf("Validate/expired delete: %v", err)
	}
	store.fail = "deleteuser"
	if err := m.RevokeAllForUser(ctx, alice); !errors.Is(err, errDB) {
		t.Errorf("RevokeAllForUser: %v", err)
	}
}

func TestSwitchOrganization_KeepsTimeoutsAndStepUpAndRotatesTheToken(t *testing.T) {
	m, store, c := manager(t)
	orgB := tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	oldToken, _, _ := m.Create(context.Background(), alice, orgA)
	c.advance(10 * time.Minute)

	newToken, s, err := m.SwitchOrganization(context.Background(), oldToken, orgB)
	if err != nil {
		t.Fatalf("SwitchOrganization() error = %v", err)
	}
	if newToken == oldToken || s.Org != orgB || s.User != alice {
		t.Errorf("session = %+v, token rotated = %v", s, newToken != oldToken)
	}
	// Switching must neither extend the absolute timeout nor count as a re-authentication.
	if !s.CreatedAt.Equal(t0) || !s.StepUpAt.Equal(t0) {
		t.Errorf("CreatedAt = %v, StepUpAt = %v, want both %v", s.CreatedAt, s.StepUpAt, t0)
	}
	if _, err := m.Validate(context.Background(), oldToken); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("old token still valid: %v", err)
	}
	if len(store.byHash) != 1 {
		t.Errorf("%d sessions stored, want 1", len(store.byHash))
	}
}

func TestSwitchOrganization_RejectsInvalidSessions(t *testing.T) {
	m, _, _ := manager(t)
	if _, _, err := m.SwitchOrganization(context.Background(), "unknown", orgA); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}
