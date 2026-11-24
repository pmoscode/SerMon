// Package logconfig is what the server sets on the agents of a node group (ADR 0030, M3-03):
// which log entries they send and the redaction rules of the organization. Pure, without I/O.
//
// The server checks everything it can before it stores and sends a configuration: the bounds of
// the protocol and the regexes (Go regexp, RE2 syntax). The agent compiles them again with the
// Rust regex crate and reports what only that one rejects.
package logconfig

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Severity is the syslog priority as in the protocol: 1 emergency … 8 debug; 0 is unspecified
// (as a filter: no filter).
type Severity uint8

// Severities, from the most to the least severe.
const (
	SeverityUnspecified Severity = iota
	SeverityEmergency
	SeverityAlert
	SeverityCritical
	SeverityError
	SeverityWarning
	SeverityNotice
	SeverityInfo
	SeverityDebug
)

// Bounds, the same as in the protocol (agent.proto, log_settings.proto).
const (
	MaxUnits        = 64
	MaxUnitBytes    = 256
	MaxFiles        = 16
	MaxGlobBytes    = 1024
	MaxRegexBytes   = 1024
	MaxRules        = 32
	MaxErrorBytes   = 1024
	maxSeverityCode = SeverityDebug
)

// FileGlob is a set of log files (M3-05).
type FileGlob struct {
	// Pattern is an absolute path with * and ? in it.
	Pattern string `json:"pattern"`
	// MultilineStart is a regex for the first line of an entry; empty: every line is one.
	MultilineStart string `json:"multiline_start,omitempty"`
}

// Settings select the log entries of the nodes of a group.
type Settings struct {
	Enabled      bool       `json:"enabled"`
	MinSeverity  Severity   `json:"min_severity,omitempty"`
	ExcludeUnits []string   `json:"exclude_units,omitempty"`
	Files        []FileGlob `json:"files,omitempty"`
}

// Default is what a group without settings gets: logs on, without filters (KONZEPT §10.1).
func Default() Settings { return Settings{Enabled: true} }

// Rule is a redaction rule of the organization (KONZEPT §10.3).
type Rule struct {
	Pattern string `json:"pattern"`
}

// NodeConfig is what the agent of one node gets.
type NodeConfig struct {
	Version  uint64
	Settings Settings
	Rules    []Rule
}

// Applied is an agent's answer to a configuration.
type Applied struct {
	Version uint64
	// Error says why the agent kept its previous configuration; empty if it took the new one.
	Error      string
	LogsVetoed bool
	At         time.Time
}

// ErrInvalid marks settings or rules the server does not accept.
var ErrInvalid = errors.New("invalid log settings")

// InvalidError says what is wrong; Index is the position in a list, -1 for the list itself.
type InvalidError struct {
	Field  string
	Index  int
	Reason string
}

func (e *InvalidError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("%s: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("%s %d: %s", e.Field, e.Index+1, e.Reason)
}

// Unwrap makes errors.Is(err, ErrInvalid) true.
func (e *InvalidError) Unwrap() error { return ErrInvalid }

func invalid(field string, index int, format string, args ...any) error {
	return &InvalidError{Field: field, Index: index, Reason: fmt.Sprintf(format, args...)}
}

// systemd unit names (with escapes like \x2d) and syslog identifiers.
var unitName = regexp.MustCompile(`^[A-Za-z0-9@:._\\-]{1,256}$`)

// Validate checks the settings against the bounds of the protocol.
func (s Settings) Validate() error {
	if s.MinSeverity > maxSeverityCode {
		return invalid("min_severity", -1, "unknown severity %d", s.MinSeverity)
	}
	if len(s.ExcludeUnits) > MaxUnits {
		return invalid("exclude_units", -1, "at most %d units", MaxUnits)
	}
	for i, unit := range s.ExcludeUnits {
		if !unitName.MatchString(unit) {
			return invalid("exclude_units", i, "not a unit name")
		}
		if slices.Contains(s.ExcludeUnits[:i], unit) {
			return invalid("exclude_units", i, "listed twice")
		}
	}
	if len(s.Files) > MaxFiles {
		return invalid("files", -1, "at most %d patterns", MaxFiles)
	}
	for i, glob := range s.Files {
		if err := glob.validate(); err != nil {
			return invalid("files", i, "%s", err)
		}
		if slices.ContainsFunc(s.Files[:i], func(g FileGlob) bool { return g.Pattern == glob.Pattern }) {
			return invalid("files", i, "listed twice")
		}
	}
	return nil
}

// neverRead are the paths the agent refuses whatever its prefixes allow (sermon-logs
// ALWAYS_DENIED, M3-28): refused here already, so the UI says so at once.
var neverRead = []string{
	"/etc/sermon", "/proc", "/sys", "/dev", "/var/log/journal", "/run/log/journal",
	"/var/log/wtmp", "/var/log/btmp", "/var/log/lastlog",
}

// deniedPath reports whether the literal start of a pattern (the segments before the first
// wildcard) lies in a path the agent never reads. Matches by wildcards are refused by the agent.
func deniedPath(segments []string) bool {
	var literal []string
	for _, s := range segments {
		if strings.ContainsAny(s, "*?") {
			break
		}
		literal = append(literal, s)
	}
	for _, d := range neverRead {
		ds := strings.Split(d[1:], "/")
		if len(literal) >= len(ds) && slices.Equal(literal[:len(ds)], ds) {
			return true
		}
	}
	return false
}

func (g FileGlob) validate() error {
	p := g.Pattern
	switch {
	case len(p) < 2 || len(p) > MaxGlobBytes:
		return fmt.Errorf("the pattern must have 2 to %d bytes", MaxGlobBytes)
	case !strings.HasPrefix(p, "/"):
		return errors.New("the pattern must be an absolute path")
	case strings.ContainsRune(p, 0):
		return errors.New("the pattern contains a NUL byte")
	case strings.Contains(p, "/../") || strings.HasSuffix(p, "/.."):
		return errors.New("the pattern must not contain .. segments")
	case strings.ContainsAny(p, `[]\`):
		// The agent knows * and ? only (M3-05).
		return errors.New("only * and ? are wildcards")
	}
	segments := strings.Split(p[1:], "/")
	for _, segment := range segments {
		if segment == "" || segment == "." {
			return errors.New("the pattern must not contain empty or . segments")
		}
	}
	if deniedPath(segments) {
		return errors.New("the agent never reads this path (its own files, /proc, /sys, /dev, the journal's files, login records)")
	}
	if len(g.MultilineStart) > MaxRegexBytes {
		return fmt.Errorf("the multiline regex is longer than %d bytes", MaxRegexBytes)
	}
	if g.MultilineStart != "" {
		if _, err := regexp.Compile(g.MultilineStart); err != nil {
			return fmt.Errorf("the multiline regex does not compile: %w", err)
		}
	}
	return nil
}

// ValidateRules checks redaction rules: at most MaxRules, each a regex of 1 to MaxRegexBytes
// bytes that compiles. Backreferences and look-around don't exist in RE2.
func ValidateRules(rules []Rule) error {
	if len(rules) > MaxRules {
		return invalid("rule", -1, "at most %d rules", MaxRules)
	}
	for i, rule := range rules {
		if rule.Pattern == "" || len(rule.Pattern) > MaxRegexBytes {
			return invalid("rule", i, "the pattern must have 1 to %d bytes", MaxRegexBytes)
		}
		if _, err := regexp.Compile(rule.Pattern); err != nil {
			return invalid("rule", i, "%s", err)
		}
	}
	return nil
}
