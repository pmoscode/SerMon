package identity_test

import (
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

func TestNormalizeEmail(t *testing.T) {
	valid := []struct{ in, want string }{
		{"\tAnn@Example.org ", "ann@example.org"},
		{"bob+tag@mail.example", "bob+tag@mail.example"},
	}
	for _, tc := range valid {
		if got, ok := identity.NormalizeEmail(tc.in); !ok || got != tc.want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{"", "no-at-sign", "@example.org", "ann@", "ann@localhost", "a b@example.org", "a@b@example.org", "ann@exa mple.org", strings.Repeat("a", 320) + "@example.org"} {
		if _, ok := identity.NormalizeEmail(in); ok {
			t.Errorf("NormalizeEmail(%q) accepted", in)
		}
	}
}
