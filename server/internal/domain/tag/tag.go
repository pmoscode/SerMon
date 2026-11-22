// Package tag holds the free tags of nodes (KONZEPT §7, M2-29): key=value pairs such as env=prod
// or role=db for filters and for matching in rules, notification policies and silences – never
// for rights.
package tag

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Limits of a node's tags.
const (
	MaxTags     = 16
	MaxKeyLen   = 32
	MaxValueLen = 63
)

var (
	keyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	valuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]*$`)
)

// ErrInvalid means tags are malformed.
var ErrInvalid = errors.New("invalid tags")

// Tags are a node's key=value pairs, or a selector of them.
type Tags map[string]string

// Validate checks the limits and the characters: keys are lowercase letters, digits, _ and -
// beginning with a letter; values letters, digits and _ . : / -, beginning with a letter or digit.
// Both are shown in the UI and in notifications, so nothing else is allowed. Messages never name
// a value.
func (t Tags) Validate() error {
	if len(t) > MaxTags {
		return fmt.Errorf("%w: at most %d tags", ErrInvalid, MaxTags)
	}
	for k, v := range t {
		if len(k) > MaxKeyLen || !keyPattern.MatchString(k) {
			return fmt.Errorf("%w: keys take 1 to %d lowercase letters, digits, _ and -, beginning with a letter", ErrInvalid, MaxKeyLen)
		}
		if len(v) > MaxValueLen || !valuePattern.MatchString(v) {
			return fmt.Errorf("%w: the value of %s takes 1 to %d letters, digits and _ . : / -", ErrInvalid, k, MaxValueLen)
		}
	}
	return nil
}

// Selects reports whether every pair of the selector is among the node's tags; an empty selector
// selects every node.
func (t Tags) Selects(node Tags) bool {
	for k, v := range t {
		if got, ok := node[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// String returns the pairs sorted by key, e.g. "env=prod,role=db" (audit details).
func (t Tags) String() string {
	pairs := make([]string, 0, len(t))
	for _, k := range slices.Sorted(maps.Keys(t)) {
		pairs = append(pairs, k+"="+t[k])
	}
	return strings.Join(pairs, ",")
}
