// Package totp implements time-based one-time passwords (RFC 6238 over RFC 4226): HMAC-SHA1,
// 30-second steps, 6 digits – what every authenticator app supports (KONZEPT §7).
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // G505: RFC 6238 default, HMAC-SHA1 is not affected by SHA-1 collisions
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Parameters of the codes.
const (
	Digits      = 6
	Period      = 30 * time.Second
	SecretBytes = 20 // 160 bits as recommended by RFC 4226
	// skew is the number of steps accepted before and after the current one (clock drift).
	skew = 1
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random secret.
func GenerateSecret() ([]byte, error) {
	secret := make([]byte, SecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate TOTP secret: %w", err)
	}
	return secret, nil
}

// EncodeSecret returns the secret in base32 without padding, for manual entry in an app.
func EncodeSecret(secret []byte) string { return encoding.EncodeToString(secret) }

// KeyURI returns the otpauth:// URI for QR codes (Google Authenticator key URI format).
func KeyURI(issuer, account string, secret []byte) string {
	query := url.Values{}
	query.Set("secret", EncodeSecret(secret))
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(Digits))
	query.Set("period", fmt.Sprint(int(Period.Seconds())))
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	return "otpauth://totp/" + label + "?" + strings.ReplaceAll(query.Encode(), "+", "%20")
}

// Step returns the time step of t.
func Step(t time.Time) int64 { return t.Unix() / int64(Period.Seconds()) }

// Code returns the code of a time step.
func Code(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step)) //nolint:gosec // G115: steps are positive
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, value%1_000_000)
}

// Verify checks a code against the steps around now, ignoring steps up to lastUsed (replay
// protection: every step works once). It returns the matched step to store as the new lastUsed.
func Verify(secret []byte, code string, now time.Time, lastUsed int64) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != Digits || strings.Trim(code, "0123456789") != "" {
		return 0, false
	}
	current := Step(now)
	matched, ok := int64(0), false
	// Check every candidate (no early exit) so the time does not reveal which step matched.
	for step := current - skew; step <= current+skew; step++ {
		if step > lastUsed && subtle.ConstantTimeCompare([]byte(Code(secret, step)), []byte(code)) == 1 && !ok {
			matched, ok = step, true
		}
	}
	return matched, ok
}
