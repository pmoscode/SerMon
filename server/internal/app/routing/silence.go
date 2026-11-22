package routing

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// SilenceID identifies a silence.
type SilenceID uuid.UUID

// String returns the UUID.
func (id SilenceID) String() string { return uuid.UUID(id).String() }

// Limits of a silence.
const (
	MaxSilenceDuration = 30 * 24 * time.Hour
	MaxSilenceNodes    = 50
	// MaxOpenSilences bounds the silences of an organization that have not ended.
	MaxOpenSilences = 200
	maxComment      = 500
)

// ErrInvalidSilence means a silence is malformed.
var ErrInvalidSilence = errors.New("invalid silence")

func invalidSilence(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSilence, fmt.Sprintf(format, args...))
}

// Silence mutes the notifications of matching alerts for a while: changes, reminders and
// escalations. The alerts themselves go on (KONZEPT §12.1).
type Silence struct {
	ID    SilenceID
	Match Matchers
	// Nodes limits the silence to these nodes of its groups.
	Nodes    []identity.NodeID
	StartsAt time.Time
	EndsAt   time.Time
	Comment  string
	// CreatedBy and ExpiredBy are who began and who ended it early.
	CreatedBy identity.UserID
	CreatedAt time.Time
	ExpiredBy identity.UserID
}

// ActiveAt reports whether the silence mutes at t.
func (s Silence) ActiveAt(t time.Time) bool { return !t.Before(s.StartsAt) && t.Before(s.EndsAt) }

// Mutes reports whether the silence selects the target (whatever the time).
func (s Silence) Mutes(t Target) bool {
	return s.Match.Match(t) && (len(s.Nodes) == 0 || slices.Contains(s.Nodes, t.Node))
}

// Validate checks a new silence at now: a selector that names something (groups, nodes, rules,
// labels or tags; never everything by accident), a period of at most 30 days that has not ended, and
// a comment. Messages name the field, never a value.
func (s *Silence) Validate(now time.Time) error {
	if err := s.Match.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSilence, err)
	}
	s.Comment = strings.TrimSpace(s.Comment)
	m := s.Match
	switch {
	case len(m.Groups) == 0 && len(m.Rules) == 0 && len(m.Labels) == 0 && len(m.Tags) == 0:
		return invalidSilence("a silence needs groups, rules, labels or tags")
	case len(s.Nodes) > 0 && len(m.Groups) == 0:
		return invalidSilence("nodes need the groups they belong to")
	case len(s.Nodes) > MaxSilenceNodes || hasDuplicates(s.Nodes):
		return invalidSilence("at most %d different nodes", MaxSilenceNodes)
	case !s.EndsAt.After(s.StartsAt) || !s.EndsAt.After(now):
		return invalidSilence("the end must lie after the start and in the future")
	case s.EndsAt.Sub(s.StartsAt) > MaxSilenceDuration:
		return invalidSilence("a silence lasts at most %v", MaxSilenceDuration)
	case s.Comment == "" || utf8.RuneCountInString(s.Comment) > maxComment:
		return invalidSilence("the comment needs 1 to %d characters", maxComment)
	case strings.IndexFunc(s.Comment, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0:
		return invalidSilence("the comment contains control characters")
	}
	return nil
}
