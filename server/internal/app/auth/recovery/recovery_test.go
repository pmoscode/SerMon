package recovery_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/app/auth/recovery"
)

func TestGenerate_ReturnsDistinctReadableCodes(t *testing.T) {
	codes, err := recovery.Generate()
	if err != nil || len(codes) != recovery.Count {
		t.Fatalf("Generate() = %v, %v", codes, err)
	}
	shape := regexp.MustCompile(`^[a-z2-7]{4}(-[a-z2-7]{4}){3}$`)
	seen := map[string]bool{}
	for _, c := range codes {
		if !shape.MatchString(c) || seen[c] {
			t.Errorf("code %q malformed or repeated", c)
		}
		seen[c] = true
		if !recovery.LooksLikeCode(c) || recovery.LooksLikeCode("123456") {
			t.Errorf("LooksLikeCode(%q)", c)
		}
	}
}

func TestHash_IgnoresCaseDashesAndSpaces(t *testing.T) {
	codes, _ := recovery.Generate()
	want := recovery.Hash(codes[0])
	for _, variant := range []string{strings.ToUpper(codes[0]), strings.ReplaceAll(codes[0], "-", ""), " " + strings.ReplaceAll(codes[0], "-", " ") + " "} {
		if !bytes.Equal(recovery.Hash(variant), want) {
			t.Errorf("Hash(%q) differs", variant)
		}
	}
	if bytes.Equal(recovery.Hash(codes[1]), want) {
		t.Error("different codes, same hash")
	}
}
