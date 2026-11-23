package logql

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// Bounds of a filter.
const (
	MaxTextBytes = 256
	MaxUnits     = 20
	maxUnitBytes = 256
	// MaxStep bounds the step of a metric query.
	MaxStep = 24 * time.Hour
)

// ErrInvalid marks a filter the builder refuses.
var ErrInvalid = errors.New("invalid log filter")

// Source is the source label of a stream (ADR 0031).
type Source string

// Sources.
const (
	SourceJournal Source = "journal"
	SourceFile    Source = "file"
)

// Severity is the severity label of a stream.
type Severity string

// Severities, from the most to the least severe; SeverityUnknown has none.
const (
	SeverityEmergency Severity = "emergency"
	SeverityAlert     Severity = "alert"
	SeverityCritical  Severity = "critical"
	SeverityError     Severity = "error"
	SeverityWarning   Severity = "warning"
	SeverityNotice    Severity = "notice"
	SeverityInfo      Severity = "info"
	SeverityDebug     Severity = "debug"
	SeverityUnknown   Severity = "unknown"
)

// Severities lists the severities from the most to the least severe, unknown last.
var Severities = []Severity{
	SeverityEmergency, SeverityAlert, SeverityCritical, SeverityError, SeverityWarning,
	SeverityNotice, SeverityInfo, SeverityDebug, SeverityUnknown,
}

// Filter selects log lines. The nodes must already be those the caller may see.
type Filter struct {
	Nodes      []identity.NodeID
	Sources    []Source
	Units      []string
	Severities []Severity
	// Text is a part of the line; with IgnoreCase regardless of case.
	Text       string
	IgnoreCase bool
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Lines is the log query of the filter: the stream selector and, with a text, a line filter.
func Lines(f Filter) (Query, error) {
	selector, err := Select(f.Nodes)
	if err != nil {
		return Query{}, err
	}
	matchers := []string{strings.TrimSuffix(selector.expr, "}")}
	if len(f.Sources) > 0 {
		values := make([]string, 0, len(f.Sources))
		for _, s := range f.Sources {
			if s != SourceJournal && s != SourceFile {
				return Query{}, invalid("unknown source %q", s)
			}
			values = append(values, string(s))
		}
		matchers = append(matchers, "source=~"+Quote(alternatives(values)))
	}
	if len(f.Units) > MaxUnits {
		return Query{}, invalid("at most %d units", MaxUnits)
	}
	if len(f.Units) > 0 {
		values := make([]string, 0, len(f.Units))
		for _, u := range f.Units {
			if u == "" || len(u) > maxUnitBytes || !utf8.ValidString(u) {
				return Query{}, invalid("a unit must have 1 to %d bytes of UTF-8", maxUnitBytes)
			}
			values = append(values, regexp.QuoteMeta(u))
		}
		matchers = append(matchers, "unit=~"+Quote(alternatives(values)))
	}
	if len(f.Severities) > 0 {
		values := make([]string, 0, len(f.Severities))
		for _, s := range f.Severities {
			if !slices.Contains(Severities, s) {
				return Query{}, invalid("unknown severity %q", s)
			}
			values = append(values, string(s))
		}
		matchers = append(matchers, "severity=~"+Quote(alternatives(values)))
	}
	expr := strings.Join(matchers, ", ") + "}"
	switch {
	case len(f.Text) > MaxTextBytes || !utf8.ValidString(f.Text):
		return Query{}, invalid("the text must have at most %d bytes of UTF-8", MaxTextBytes)
	case f.Text == "":
	case f.IgnoreCase:
		expr += " |~ " + Quote("(?i)"+regexp.QuoteMeta(f.Text))
	default:
		expr += " |= " + Quote(f.Text)
	}
	return Query{expr: expr}, nil
}

// CountBySeverity counts the lines of the filter per severity and step (a histogram).
func CountBySeverity(f Filter, step time.Duration) (Query, error) {
	if step < time.Second || step > MaxStep {
		return Query{}, invalid("the step must be between 1 s and %v", MaxStep)
	}
	lines, err := Lines(f)
	if err != nil {
		return Query{}, err
	}
	seconds := strconv.FormatInt(int64(step/time.Second), 10)
	return Query{expr: "sum by (severity) (count_over_time(" + lines.expr + " [" + seconds + "s]))"}, nil
}

// alternatives joins sorted, distinct values for a regex matcher.
func alternatives(values []string) string {
	slices.Sort(values)
	return strings.Join(slices.Compact(values), "|")
}
