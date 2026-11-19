package password_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
)

// fast parameters keep the tests quick; production uses password.DefaultParams.
var fast = password.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32}

func hasher(t *testing.T, p password.Params) *password.Hasher {
	t.Helper()
	h, err := password.NewHasher(p)
	if err != nil {
		t.Fatalf("NewHasher() error = %v", err)
	}
	return h
}

func TestDefaultParams_MatchTheConcept(t *testing.T) {
	p := password.DefaultParams
	if p.Memory != 64*1024 || p.Time != 3 || p.Threads != 1 || p.SaltLength < 16 || p.KeyLength < 32 {
		t.Errorf("DefaultParams = %+v, want argon2id m=64MiB t=3 p=1 (KONZEPT §7)", p)
	}
}

func TestHash_ProducesAPHCStringWithAFreshSalt(t *testing.T) {
	h := hasher(t, fast)

	a, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	b, _ := h.Hash("correct horse battery staple")

	if !strings.HasPrefix(a, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Errorf("hash %q is not an argon2id PHC string", a)
	}
	if a == b {
		t.Error("two hashes of the same password must differ (random salt)")
	}
}

func TestVerify(t *testing.T) {
	h := hasher(t, fast)
	encoded, _ := h.Hash("correct horse battery staple")

	if ok, err := h.Verify("correct horse battery staple", encoded); !ok || err != nil {
		t.Errorf("Verify(correct) = %v, %v; want true", ok, err)
	}
	for _, wrong := range []string{"correct horse battery stapl", "Correct horse battery staple", ""} {
		if ok, err := h.Verify(wrong, encoded); ok || err != nil {
			t.Errorf("Verify(%q) = %v, %v; want false, nil", wrong, ok, err)
		}
	}
}

func TestVerify_UsesTheParametersStoredInTheHash(t *testing.T) {
	old := hasher(t, fast)
	encoded, _ := old.Hash("correct horse battery staple")
	current := hasher(t, password.Params{Memory: 16 * 1024, Time: 2, Threads: 1, SaltLength: 16, KeyLength: 32})

	if ok, err := current.Verify("correct horse battery staple", encoded); !ok || err != nil {
		t.Errorf("hashes with older parameters must still verify: %v, %v", ok, err)
	}
	if !current.NeedsRehash(encoded) {
		t.Error("NeedsRehash must report hashes with outdated parameters")
	}
	fresh, _ := current.Hash("x-correct horse battery staple")
	if current.NeedsRehash(fresh) {
		t.Error("NeedsRehash must not report hashes with current parameters")
	}
}

func TestVerify_RejectsMalformedHashes(t *testing.T) {
	h := hasher(t, fast)
	for _, encoded := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaA",  // wrong variant
		"$argon2id$v=18$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaA", // wrong version
		"$argon2id$v=19$m=x,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8192,t=1,p=1$!!!$aGFzaA",
		"$argon2id$v=19$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$!!!",
		"$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaA", // absurd memory (DoS)
	} {
		if ok, err := h.Verify("whatever password", encoded); ok || !errors.Is(err, password.ErrMalformedHash) {
			t.Errorf("Verify(%q) = %v, %v; want false, ErrMalformedHash", encoded, ok, err)
		}
		if !h.NeedsRehash(encoded) {
			t.Errorf("NeedsRehash(%q) must be true for unusable hashes", encoded)
		}
	}
}

func TestVerifyDummy_NeverSucceeds(t *testing.T) {
	h := hasher(t, fast)
	if h.VerifyDummy("anything") {
		t.Error("VerifyDummy must always report false")
	}
}

func TestNewHasher_RejectsWeakParameters(t *testing.T) {
	for name, p := range map[string]password.Params{
		"too little memory": {Memory: 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32},
		"zero time":         {Memory: 8192, Time: 0, Threads: 1, SaltLength: 16, KeyLength: 32},
		"zero threads":      {Memory: 8192, Time: 1, Threads: 0, SaltLength: 16, KeyLength: 32},
		"short salt":        {Memory: 8192, Time: 1, Threads: 1, SaltLength: 8, KeyLength: 32},
		"short key":         {Memory: 8192, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 16},
	} {
		if _, err := password.NewHasher(p); err == nil {
			t.Errorf("%s: NewHasher() error = nil", name)
		}
	}
}

func TestHash_RejectsPasswordsAboveTheMaximumLength(t *testing.T) {
	if _, err := hasher(t, fast).Hash(strings.Repeat("a", password.MaxLength+1)); !errors.Is(err, password.ErrTooLong) {
		t.Errorf("Hash(too long) error = %v, want ErrTooLong", err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name     string
		password string
		context  []string
		want     error
	}{
		{"good passphrase", "correct horse battery staple", nil, nil},
		{"12 characters", "Tr0ub4dor&3x", nil, nil},
		{"unicode counts characters, not bytes", "äöüäöüäöüäöü", nil, nil},
		{"too short", "Tr0ub4dor&3", nil, password.ErrTooShort},
		{"too long", strings.Repeat("a", password.MaxLength+1), nil, password.ErrTooLong},
		{"common password", "qwertyuiopasdfghjkl", nil, password.ErrCommon},
		{"common password, other case", "QwertyUiopasdfghjkl", nil, password.ErrCommon},
		{"contains e-mail name", "alice-secret-password", []string{"alice@example.org"}, password.ErrContainsContext},
		{"contains display name", "my name is Alice Example!", []string{"Alice Example"}, password.ErrContainsContext},
		{"short context words are ignored", "a long and unrelated passphrase", []string{"al@x.io"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := password.Validate(tc.password, tc.context...)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Errorf("Validate(%q) error = %v, want %v", tc.password, err, tc.want)
			}
		})
	}
}

func TestCommonList_IsLoaded(t *testing.T) {
	if n := password.CommonPasswordCount(); n < 1000 {
		t.Errorf("only %d common passwords loaded, want > 1000", n)
	}
}
