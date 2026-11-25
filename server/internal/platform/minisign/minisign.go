// Package minisign checks signatures of the release key (ADR 0012, ADR 0036): minisign with the
// algorithm "Ed" (Ed25519 over the file, not pre-hashed), the key ID and the global signature
// over the trusted comment, which names the file and the version.
package minisign

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
)

// ErrInvalid means a key or signature is malformed or doesn't match.
var ErrInvalid = errors.New("invalid minisign signature")

// PublicKey is a release key.
type PublicKey struct {
	id  [8]byte
	key ed25519.PublicKey
}

// ParseKey reads a key as release-sign.sh prints it (base64 of "Ed", ID and key).
func ParseKey(text string) (PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(raw) != 42 || !bytes.HasPrefix(raw, []byte("Ed")) {
		return PublicKey{}, ErrInvalid
	}
	var k PublicKey
	copy(k.id[:], raw[2:10])
	k.key = ed25519.PublicKey(bytes.Clone(raw[10:]))
	return k, nil
}

// Signed is what a valid signature's trusted comment says.
type Signed struct {
	File, Version string
}

// Verify checks signature (a .minisig file) over data with key and returns its trusted comment's
// file and version.
func Verify(key PublicKey, signature, data []byte) (Signed, error) {
	lines := strings.Split(strings.ReplaceAll(string(signature), "\r\n", "\n"), "\n")
	if len(lines) < 4 {
		return Signed{}, ErrInvalid
	}
	sig, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(sig) != 74 || !bytes.HasPrefix(sig, []byte("Ed")) || !bytes.Equal(sig[2:10], key.id[:]) {
		return Signed{}, ErrInvalid
	}
	trusted, ok := strings.CutPrefix(lines[2], "trusted comment: ")
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if !ok || err != nil || len(global) != ed25519.SignatureSize {
		return Signed{}, ErrInvalid
	}
	if !ed25519.Verify(key.key, data, sig[10:]) || !ed25519.Verify(key.key, append(bytes.Clone(sig[10:]), trusted...), global) {
		return Signed{}, ErrInvalid
	}
	var out Signed
	for field := range strings.SplitSeq(trusted, "\t") {
		if v, ok := strings.CutPrefix(field, "file:"); ok {
			out.File = v
		}
		if v, ok := strings.CutPrefix(field, "version:"); ok {
			out.Version = v
		}
	}
	if out.File == "" || out.Version == "" {
		return Signed{}, ErrInvalid
	}
	return out, nil
}
