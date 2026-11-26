// Package textsafe makes text from hosts safe to show: SSH user names, sudo commands, process and
// host names come from machines (and, for user names, from anyone on the internet). Control and
// format characters (bidi overrides, zero width) and line or paragraph separators would reorder or
// forge what a reader sees in the UI and in notifications (Trojan Source, M2-28).
package textsafe

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Invisible reports whether r changes the display without being visible itself: control (Cc),
// format (Cf: bidi marks and overrides, zero width, BOM), line or paragraph separators, and the
// Hangul fillers that render blank (review M4-30, like the UI's displayText).
func Invisible(r rune) bool {
	switch r {
	case '\u115f', '\u1160', '\u3164', '\uffa0':
		return true
	}
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
}

// Visible replaces every invisible character with U+FFFD, so it stays noticeable.
func Visible(s string) string {
	if !strings.ContainsFunc(s, Invisible) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if Invisible(r) {
			return utf8.RuneError
		}
		return r
	}, s)
}
