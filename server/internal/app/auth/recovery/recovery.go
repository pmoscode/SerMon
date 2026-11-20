// Package recovery creates the one-time recovery codes of two-factor authentication (KONZEPT §7).
// A code has 80 random bits, so a fast hash suffices to store it: guessing it from the hash is
// as hard as guessing the code.
package recovery

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
)

// Count is the number of codes per set.
const Count = 10

const (
	codeBytes = 10 // 80 bits → 16 base32 characters
	groupSize = 4
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Generate returns a new set of codes, formatted for reading ("abcd-efgh-ijkl-mnop").
func Generate() ([]string, error) {
	codes := make([]string, Count)
	for i := range codes {
		raw := make([]byte, codeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("generate recovery code: %w", err)
		}
		plain := strings.ToLower(encoding.EncodeToString(raw))
		groups := make([]string, 0, len(plain)/groupSize)
		for j := 0; j < len(plain); j += groupSize {
			groups = append(groups, plain[j:j+groupSize])
		}
		codes[i] = strings.Join(groups, "-")
	}
	return codes, nil
}

// Hash returns the stored form of a code; case, dashes and spaces don't matter.
func Hash(code string) []byte {
	normalized := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte("sermon-recovery-code:" + normalized))
	return sum[:]
}

// LooksLikeCode reports whether input has the shape of a recovery code (not a TOTP code).
func LooksLikeCode(input string) bool {
	return len(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(input))) == 16
}
