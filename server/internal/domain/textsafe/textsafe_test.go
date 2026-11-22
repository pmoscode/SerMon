package textsafe_test

import (
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// Text from hosts (SSH user names, sudo commands, host names) is shown in the UI and in
// notifications: control, format (bidi overrides, zero width) and line/paragraph separators
// would reorder or forge what a reader sees (Trojan Source, M2-28).
func TestVisible_ReplacesWhatAReaderCannotSee(t *testing.T) {
	for in, want := range map[string]string{
		"alice":                 "alice",
		"jürgen":                "jürgen",
		"\u202etoor":            "\ufffdtoor",
		"ro\u200bot":            "ro\ufffdot",
		"a\nAddress: x":         "a\ufffdAddress: x",
		"a\u2028b\u2029c":       "a\ufffdb\ufffdc",
		"\u2066x\u2069":         "\ufffdx\ufffd",
		"\xef\xbb\xbfbom\u061c": "\ufffdbom\ufffd",
		"tab\there\x7f":         "tab\ufffdhere\ufffd",
	} {
		if got := textsafe.Visible(in); got != want {
			t.Errorf("Visible(%q) = %q, want %q", in, got, want)
		}
	}
	if !textsafe.Invisible('\u202e') || textsafe.Invisible('ü') || textsafe.Invisible(' ') {
		t.Error("Invisible classifies wrongly")
	}
}
