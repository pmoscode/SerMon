package minisign_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/platform/minisign"
)

// Sign writes a .minisig of data like scripts/release-sign.sh; the public key line is returned
// for ParseKey.
func Sign(t *testing.T, priv ed25519.PrivateKey, id byte, data []byte, file, version string) []byte {
	t.Helper()
	ids := bytes.Repeat([]byte{id}, 8)
	sig := ed25519.Sign(priv, data)
	trusted := "timestamp:1\tfile:" + file + "\thashed:0\tversion:" + version
	global := ed25519.Sign(priv, append(bytes.Clone(sig), trusted...))
	return []byte("untrusted comment: test\n" + base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), ids...), sig...)) +
		"\ntrusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

func keyLine(pub ed25519.PublicKey, id byte) string {
	return base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), bytes.Repeat([]byte{id}, 8)...), pub...))
}

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, err := minisign.ParseKey(keyLine(pub, 1))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("\x7fELF agent")
	sig := Sign(t, priv, 1, data, "sermon-agent-linux-arm64", "1.2.3")
	got, err := minisign.Verify(key, sig, data)
	if err != nil || got != (minisign.Signed{File: "sermon-agent-linux-arm64", Version: "1.2.3"}) {
		t.Fatalf("Verify() = %+v, %v", got, err)
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	for name, bad := range map[string][]byte{
		"changed file":   nil,
		"other key":      Sign(t, otherPriv, 1, data, "sermon-agent-linux-arm64", "1.2.3"),
		"other key id":   Sign(t, priv, 2, data, "sermon-agent-linux-arm64", "1.2.3"),
		"forged comment": []byte(strings.Replace(string(sig), "version:1.2.3", "version:9.9.9", 1)),
		"no version":     Sign(t, priv, 1, data, "sermon-agent-linux-arm64", ""),
		"truncated":      sig[:40],
		"not base64":     []byte("a\n!!\nb\nc\n"),
	} {
		in := data
		if bad == nil {
			bad, in = sig, []byte("\x7fELF agenT")
		}
		if _, err := minisign.Verify(key, bad, in); !errors.Is(err, minisign.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, bad := range []string{"", "aGk=", base64.StdEncoding.EncodeToString(make([]byte, 42))} {
		if _, err := minisign.ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) succeeded", bad)
		}
	}
}
