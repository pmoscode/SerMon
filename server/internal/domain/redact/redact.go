// Package redact is the server's copy of the agent's redaction (KONZEPT §10.3, sermon-security
// redact.rs), for the test of rules in the UI (M3-12): the built-in patterns, then the rules of
// the organization, each replacing its match or its group named secret with ***.
//
// The agent redacts what it sends; this copy only shows what it would do. Both run the shared
// cases of testdata/redaction. Go's RE2 and Rust's regex crate agree on the syntax used here;
// \b, \w and \s are ASCII in Go and Unicode in Rust, which only matters next to non-ASCII text.
package redact

import (
	"fmt"
	"regexp"
	"strings"
)

// Mask replaces a secret.
const Mask = "***"

// Bounds of the rules, as in the agent.
const (
	MaxRules        = 32
	MaxPatternBytes = 1024
)

const keys = `password|passwd|pwd|pass|secret|token|api[-_]?key|access[-_]?key|private[-_]?key`

type builtin struct {
	re          *regexp.Regexp
	replacement string
}

// The built-in patterns in the agent's order.
var builtins = []builtin{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), Mask},
	{regexp.MustCompile(`^[A-Za-z0-9+/]{60,}={0,2}$`), Mask},
	{regexp.MustCompile(`(?i)(\b(?:[a-z0-9]+[_-])*(?:` + keys + `)\b["']?\s*[=:]\s*)(?:'[^']*'|"[^"]*"|\S+)`), "${1}" + Mask},
	{regexp.MustCompile(`(?i)(--(?:password|passwd|pass|secret|token|api[-_]?key)\s+)(?:'[^']*'|"[^"]*"|\S+)`), "${1}" + Mask},
	{regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic)\s+)\S+`), "${1}" + Mask},
	{regexp.MustCompile(`(://[^:/@\s]+:)[^@\s]+@`), "${1}" + Mask + "@"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b` +
		`|\beyJ[A-Za-z0-9_-]{4,}\.eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]+` +
		`|\b(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}` +
		`|\bxox[abprs]-[A-Za-z0-9-]{10,}` +
		`|\bsme_[A-Za-z0-9_-]{8,}`), Mask},
}

// RuleError names the first rule that cannot be used; Index counts from 0.
type RuleError struct {
	Index  int
	Reason string
}

func (e *RuleError) Error() string {
	if e.Index < 0 {
		return e.Reason
	}
	return fmt.Sprintf("rule %d: %s", e.Index+1, e.Reason)
}

// Redactor applies the built-in patterns and the rules.
type Redactor struct {
	rules []*regexp.Regexp
}

// New compiles the rules of an organization.
func New(rules []string) (*Redactor, error) {
	if len(rules) > MaxRules {
		return nil, &RuleError{Index: -1, Reason: fmt.Sprintf("at most %d rules", MaxRules)}
	}
	r := &Redactor{rules: make([]*regexp.Regexp, 0, len(rules))}
	for i, pattern := range rules {
		switch {
		case pattern == "":
			return nil, &RuleError{Index: i, Reason: "the pattern is empty"}
		case len(pattern) > MaxPatternBytes:
			return nil, &RuleError{Index: i, Reason: fmt.Sprintf("the pattern is longer than %d bytes", MaxPatternBytes)}
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, &RuleError{Index: i, Reason: err.Error()}
		}
		r.rules = append(r.rules, re)
	}
	return r, nil
}

// Redact returns text with every secret replaced by Mask.
func (r *Redactor) Redact(text string) string {
	for _, b := range builtins {
		text = b.re.ReplaceAllString(text, b.replacement)
	}
	for _, rule := range r.rules {
		text = apply(rule, text)
	}
	return text
}

// apply masks the matches of rule, or their group secret; empty matches and matches without the
// group (if the rule has one) change nothing.
func apply(rule *regexp.Regexp, text string) string {
	secret := rule.SubexpIndex("secret")
	var out strings.Builder
	copied := 0
	for _, m := range rule.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[0], m[1]
		if secret >= 0 {
			start, end = m[2*secret], m[2*secret+1]
		}
		// Empty, or a secret group that did not take part (-1, -1).
		if start == end {
			continue
		}
		out.WriteString(text[copied:start])
		out.WriteString(Mask)
		copied = end
	}
	if copied == 0 && out.Len() == 0 {
		return text
	}
	out.WriteString(text[copied:])
	return out.String()
}
