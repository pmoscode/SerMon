// Package naming validates display names (groups, organizations, users).
package naming

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clean trims a display name and checks that it has 1 to maxRunes characters and no control
// characters (names are shown in the UI, logs and notifications).
func Clean(name string, maxRunes int) (string, bool) {
	name = strings.TrimSpace(name)
	ok := name != "" && utf8.RuneCountInString(name) <= maxRunes && !strings.ContainsFunc(name, unicode.IsControl)
	return name, ok
}
