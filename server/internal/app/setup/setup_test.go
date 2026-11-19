package setup_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/setup"
)

var t0 = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// Test fixtures, not secrets.
const (
	goodPassword    = "a very long passphrase 42" // betterleaks:allow
	shortPassword   = "short"                     // betterleaks:allow
	contextPassword = "ann@example.org-1234"      // betterleaks:allow
)

type fakeStore struct {
	completed bool
	err       error
	hash      [32]byte
	expires   time.Time
	now       time.Time
	admin     setup.FirstAdmin
	calls     int
}

func (f *fakeStore) Completed(context.Context) (bool, error) { return f.completed, f.err }

func (f *fakeStore) IssueToken(_ context.Context, hash [32]byte, expires time.Time) error {
	f.calls++
	f.hash, f.expires = hash, expires
	return f.err
}

func (f *fakeStore) Complete(_ context.Context, hash [32]byte, now time.Time, admin setup.FirstAdmin) error {
	f.calls++
	f.hash, f.now, f.admin = hash, now, admin
	return f.err
}

type fakeHasher struct{}

func (fakeHasher) Hash(pw string) (string, error) { return "$argon2id$fake$" + pw, nil }

func service(store *fakeStore) *setup.Service {
	return setup.NewService(store, fakeHasher{}, func() time.Time { return t0 }, uuid.New)
}

func validRequest(token string) setup.Request {
	return setup.Request{
		Token: token, Email: " Ann@Example.org ", DisplayName: "Ann", Password: goodPassword, OrganizationName: "Example",
	}
}

func TestIssueToken_StoresOnlyTheHashWithA30MinuteLifetime(t *testing.T) {
	store := &fakeStore{}
	token, expires, err := service(store).IssueToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := service(&fakeStore{}).IssueToken(context.Background())
	if len(token) != 43 || token == other {
		t.Errorf("token %q must be 256 bit (base64url) and random", token)
	}
	if store.hash != sha256.Sum256([]byte(token)) {
		t.Error("the store must receive the SHA-256 of the token")
	}
	if want := t0.Add(30 * time.Minute); !expires.Equal(want) || !store.expires.Equal(want) {
		t.Errorf("expires = %v / %v, want %v", expires, store.expires, want)
	}
}

func TestIssueToken_RefusesAnInitializedInstance(t *testing.T) {
	_, _, err := service(&fakeStore{err: setup.ErrAlreadyInitialized}).IssueToken(context.Background())
	if !errors.Is(err, setup.ErrAlreadyInitialized) {
		t.Errorf("error = %v, want ErrAlreadyInitialized", err)
	}
}

func TestComplete_CreatesTheFirstAdmin(t *testing.T) {
	store := &fakeStore{}
	token := strings.Repeat("A", 43)

	if err := service(store).Complete(context.Background(), validRequest(token)); err != nil {
		t.Fatal(err)
	}
	a := store.admin
	if store.hash != sha256.Sum256([]byte(token)) || !store.now.Equal(t0) {
		t.Errorf("hash/now not passed to the store")
	}
	if a.Email != "ann@example.org" || a.DisplayName != "Ann" || a.OrganizationName != "Example" ||
		a.PasswordHash != "$argon2id$fake$"+goodPassword || a.User.IsZero() || a.Org == [16]byte{} {
		t.Errorf("admin = %+v", a)
	}
}

func TestComplete_ValidatesInputBeforeTouchingTheStore(t *testing.T) {
	token := strings.Repeat("A", 43)
	cases := map[string]struct {
		mutate func(*setup.Request)
		want   error
	}{
		"malformed token":  {func(r *setup.Request) { r.Token = "short" }, setup.ErrInvalidToken},
		"invalid email":    {func(r *setup.Request) { r.Email = "no-at-sign" }, setup.ErrInvalidEmail},
		"empty name":       {func(r *setup.Request) { r.DisplayName = " " }, setup.ErrInvalidName},
		"control in org":   {func(r *setup.Request) { r.OrganizationName = "a\x00b" }, setup.ErrInvalidName},
		"short password":   {func(r *setup.Request) { r.Password = shortPassword }, password.ErrTooShort},
		"password = email": {func(r *setup.Request) { r.Password = contextPassword }, password.ErrContainsContext},
	}
	for name, tc := range cases {
		store := &fakeStore{}
		req := validRequest(token)
		tc.mutate(&req)
		if err := service(store).Complete(context.Background(), req); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.want)
		}
		if store.calls != 0 {
			t.Errorf("%s: store called despite invalid input", name)
		}
	}
}

func TestComplete_PropagatesAnInvalidOrExpiredToken(t *testing.T) {
	err := service(&fakeStore{err: setup.ErrInvalidToken}).Complete(context.Background(), validRequest(strings.Repeat("A", 43)))
	if !errors.Is(err, setup.ErrInvalidToken) {
		t.Errorf("error = %v, want ErrInvalidToken", err)
	}
}

func TestRequired(t *testing.T) {
	for _, completed := range []bool{false, true} {
		required, err := service(&fakeStore{completed: completed}).Required(context.Background())
		if err != nil || required == completed {
			t.Errorf("completed=%v: Required() = %v, %v", completed, required, err)
		}
	}
}
