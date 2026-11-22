// Package naming validates display names (groups, organizations, users).
package naming

import (
	"strings"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// Clean trims a display name and checks that it has 1 to maxRunes characters and no control,
// format (bidi, zero width) or separator characters – names are shown in the UI, logs and
// notifications (textsafe, M2-28).
func Clean(name string, maxRunes int) (string, bool) {
	name = strings.TrimSpace(name)
	ok := name != "" && utf8.RuneCountInString(name) <= maxRunes && !strings.ContainsFunc(name, textsafe.Invisible)
	return name, ok
}
