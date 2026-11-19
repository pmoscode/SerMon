// Package password hashes and checks user passwords (KONZEPT §7): Argon2id in PHC string format,
// a length policy and an offline list of common passwords.
package password

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Length policy. MaxLength bounds hashing cost (bytes, not characters).
const (
	MinLength = 12
	MaxLength = 1024
	// contextMinLength: shorter parts of e-mail or name are too generic to reject a password.
	contextMinLength = 4
)

// Limits for parameters read from stored hashes (protects against crafted hashes).
const (
	minMemory = 8 * 1024
	maxMemory = 1024 * 1024
	maxTime   = 16
	maxPart   = 1024 // maximum salt/key length in a stored hash
)

var (
	// ErrTooShort means the password has fewer than MinLength characters.
	ErrTooShort = fmt.Errorf("password must have at least %d characters", MinLength)
	// ErrTooLong means the password is longer than MaxLength bytes.
	ErrTooLong = fmt.Errorf("password must not exceed %d bytes", MaxLength)
	// ErrCommon means the password is on the list of common passwords.
	ErrCommon = errors.New("password is too common")
	// ErrContainsContext means the password contains the user's e-mail or name.
	ErrContainsContext = errors.New("password must not contain your e-mail address or name")
	// ErrMalformedHash means a stored hash cannot be used.
	ErrMalformedHash = errors.New("malformed password hash")
)

// Params are the Argon2id parameters. Memory is in KiB.
type Params struct {
	Memory     uint32
	Time       uint32
	Threads    uint8
	SaltLength uint32
	KeyLength  uint32
}

// DefaultParams are Argon2id with 64 MiB, 3 iterations and 1 thread (KONZEPT §7).
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 1, SaltLength: 16, KeyLength: 32}

// Hasher hashes and verifies passwords.
type Hasher struct {
	params Params
	dummy  string
}

// NewHasher validates the parameters and prepares a dummy hash for unknown users.
func NewHasher(p Params) (*Hasher, error) {
	switch {
	case p.Memory < minMemory || p.Memory > maxMemory:
		return nil, fmt.Errorf("argon2 memory must be %d..%d KiB", minMemory, maxMemory)
	case p.Time == 0 || p.Time > maxTime:
		return nil, fmt.Errorf("argon2 time must be 1..%d", maxTime)
	case p.Threads == 0:
		return nil, errors.New("argon2 threads must be at least 1")
	case p.SaltLength < 16:
		return nil, errors.New("salt must be at least 16 bytes")
	case p.KeyLength < 32:
		return nil, errors.New("key must be at least 32 bytes")
	}
	h := &Hasher{params: p}
	dummy, err := h.Hash("dummy password for unknown users")
	if err != nil {
		return nil, err
	}
	h.dummy = dummy
	return h, nil
}

// Hash returns the PHC string $argon2id$v=19$m=…,t=…,p=…$<salt>$<hash>.
func (h *Hasher) Hash(password string) (string, error) {
	if len(password) > MaxLength {
		return "", ErrTooLong
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.Memory, h.params.Threads, h.params.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		h.params.Memory, h.params.Time, h.params.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches the encoded hash (constant-time comparison).
func (h *Hasher) Verify(password, encoded string) (bool, error) {
	p, salt, key, err := decode(encoded)
	if err != nil {
		return false, err
	}
	if len(password) > MaxLength {
		return false, nil
	}
	candidate := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLength)
	return subtle.ConstantTimeCompare(candidate, key) == 1, nil
}

// VerifyDummy spends the same time as Verify and always fails. Call it for unknown users so that
// response times do not reveal which e-mail addresses exist.
func (h *Hasher) VerifyDummy(password string) bool {
	_, _ = h.Verify(password, h.dummy)
	return false
}

// NeedsRehash reports whether a stored hash should be replaced after the next successful login.
func (h *Hasher) NeedsRehash(encoded string) bool {
	p, salt, key, err := decode(encoded)
	if err != nil {
		return true
	}
	return p.Memory != h.params.Memory || p.Time != h.params.Time || p.Threads != h.params.Threads ||
		len(salt) != int(h.params.SaltLength) || len(key) != int(h.params.KeyLength)
}

var b64 = base64.RawStdEncoding

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	if p.Memory < minMemory || p.Memory > maxMemory || p.Time == 0 || p.Time > maxTime || p.Threads == 0 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	salt, errSalt := b64.DecodeString(parts[4])
	key, errKey := b64.DecodeString(parts[5])
	if errSalt != nil || errKey != nil || len(salt) < 8 || len(salt) > maxPart || len(key) < 16 || len(key) > maxPart {
		return Params{}, nil, nil, ErrMalformedHash
	}
	p.SaltLength, p.KeyLength = uint32(len(salt)), uint32(len(key)) //nolint:gosec // G115: both bounded by maxPart above
	return p, salt, key, nil
}

// Validate checks a new password against the policy. contextWords are the user's e-mail address
// and name; the password must not contain them (or the local part of the e-mail address).
func Validate(password string, contextWords ...string) error {
	if len(password) > MaxLength {
		return ErrTooLong
	}
	if utf8.RuneCountInString(password) < MinLength {
		return ErrTooShort
	}
	lower := strings.ToLower(password)
	if _, common := commonPasswords[lower]; common {
		return ErrCommon
	}
	for _, word := range contextWords {
		for _, part := range contextParts(word) {
			if utf8.RuneCountInString(part) >= contextMinLength && strings.Contains(lower, part) {
				return ErrContainsContext
			}
		}
	}
	return nil
}

// contextParts returns the lowercase word itself and, for e-mail addresses, the local part.
func contextParts(word string) []string {
	w := strings.ToLower(strings.TrimSpace(word))
	parts := []string{w}
	if local, _, isEmail := strings.Cut(w, "@"); isEmail {
		parts = append(parts, local)
	}
	return parts
}

//go:embed common-passwords.txt
var commonPasswordsFile string

var commonPasswords = loadCommonPasswords()

func loadCommonPasswords() map[string]struct{} {
	set := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(commonPasswordsFile))
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" && !strings.HasPrefix(line, "#") {
			set[line] = struct{}{}
		}
	}
	return set
}

// CommonPasswordCount returns the number of loaded common passwords.
func CommonPasswordCount() int { return len(commonPasswords) }
