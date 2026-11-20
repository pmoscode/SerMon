package totp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/auth/totp"
)

// RFC 6238 appendix B (SHA-1 secret "12345678901234567890"); the RFC lists 8 digits, SerMon uses
// the last 6 of them.
var rfcSecret = []byte("12345678901234567890")

func TestCode_MatchesTheRFC6238TestVectors(t *testing.T) {
	for _, v := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		if got := totp.Code(rfcSecret, totp.Step(time.Unix(v.unix, 0))); got != v.want {
			t.Errorf("T=%d: %s, want %s", v.unix, got, v.want)
		}
	}
}

func TestVerify_AcceptsOneStepOfClockSkewAndRejectsReplays(t *testing.T) {
	now := time.Unix(1111111111, 0)
	step := totp.Step(now)
	for _, offset := range []int64{-1, 0, 1} {
		got, ok := totp.Verify(rfcSecret, totp.Code(rfcSecret, step+offset), now, 0)
		if !ok || got != step+offset {
			t.Errorf("offset %d: %d, %v", offset, got, ok)
		}
	}
	for _, offset := range []int64{-2, 2} {
		if _, ok := totp.Verify(rfcSecret, totp.Code(rfcSecret, step+offset), now, 0); ok {
			t.Errorf("offset %d accepted", offset)
		}
	}
	// A code whose step was already used (or an older one) is a replay.
	if _, ok := totp.Verify(rfcSecret, totp.Code(rfcSecret, step), now, step); ok {
		t.Error("replay of the last used step accepted")
	}
	if _, ok := totp.Verify(rfcSecret, totp.Code(rfcSecret, step-1), now, step); ok {
		t.Error("older step after a newer one accepted")
	}
	if got, ok := totp.Verify(rfcSecret, totp.Code(rfcSecret, step+1), now, step); !ok || got != step+1 {
		t.Error("the next step after the last used one must work")
	}
}

func TestVerify_RejectsMalformedCodes(t *testing.T) {
	now := time.Unix(59, 0)
	for _, code := range []string{"", "28708", "2870822", "28708a", "２８７０８２"} {
		if _, ok := totp.Verify(rfcSecret, code, now, 0); ok {
			t.Errorf("%q accepted", code)
		}
	}
	for _, code := range []string{"287 082", " 287082 "} {
		if _, ok := totp.Verify(rfcSecret, code, now, 0); !ok {
			t.Errorf("%q: spaces as apps show them must be accepted", code)
		}
	}
}

func TestSecretAndKeyURI(t *testing.T) {
	secret, err := totp.GenerateSecret()
	if err != nil || len(secret) != totp.SecretBytes {
		t.Fatalf("GenerateSecret() = %d bytes, %v", len(secret), err)
	}
	other, _ := totp.GenerateSecret()
	if string(other) == string(secret) {
		t.Error("secrets repeat")
	}
	encoded := totp.EncodeSecret(rfcSecret)
	if encoded != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Errorf("EncodeSecret() = %s", encoded)
	}
	uri := totp.KeyURI("SerMon", "mia@example.org", rfcSecret)
	want := "otpauth://totp/SerMon:mia@example.org?algorithm=SHA1&digits=6&issuer=SerMon&period=30&secret=" + encoded
	if uri != want {
		t.Errorf("KeyURI() = %s\nwant %s", uri, want)
	}
	if strings.Contains(totp.KeyURI("Ser Mon:x", "a b", rfcSecret), " ") {
		t.Error("labels must be escaped")
	}
}
