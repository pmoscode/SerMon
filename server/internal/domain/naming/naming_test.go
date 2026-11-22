package naming_test

import (
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/naming"
)

func TestClean(t *testing.T) {
	if got, ok := naming.Clean("  web servers ", 100); !ok || got != "web servers" {
		t.Errorf("Clean() = %q, %v", got, ok)
	}
	if got, ok := naming.Clean("Ünïcödé 名前", 10); !ok || got != "Ünïcödé 名前" {
		t.Errorf("runes, not bytes, are counted: %q, %v", got, ok)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 101), "a\nb", "tab\there", "bell\x07", "nul\x00", "Alpha\u202eGA", "zero\u200bwidth", "line\u2028sep"} {
		if _, ok := naming.Clean(bad, 100); ok {
			t.Errorf("Clean(%q) accepted", bad)
		}
	}
}
