// Package routing decides which channels hear of an alert (KONZEPT §12.1, M2-14): notification
// policies match severity, group, rule and the rule's labels; they repeat what keeps firing,
// escalate what nobody acknowledges and throttle what flaps. Without policies an organization's
// alerts go to all its enabled channels. The Router is the engine's Sink; Plan is its pure core.
package routing

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// PolicyID identifies a policy.
type PolicyID uuid.UUID

// String returns the UUID.
func (id PolicyID) String() string { return uuid.UUID(id).String() }

// Grouping is how the notifications of one round reach a channel.
type Grouping string

// Groupings.
const (
	// GroupByNode sends one message per node: several alerts of a node become a digest.
	GroupByNode Grouping = "node"
	// GroupDigest sends one message per round for all alerts.
	GroupDigest Grouping = "digest"
)

// Limits of a policy.
const (
	MaxPolicies      = 100
	MaxChannels      = 10
	MaxMatchValues   = 20
	MaxLabels        = 16
	MinRepeat        = 5 * time.Minute
	MaxRepeat        = 7 * 24 * time.Hour
	MinEscalation    = time.Minute
	MaxEscalation    = 24 * time.Hour
	maxName          = 100
	maxPattern       = 100
	maxLabelValueLen = 100
)

var (
	rulePattern = regexp.MustCompile(`^[a-z0-9_.*]+$`)
	labelKey    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// Matchers select alerts; every set matcher must match, an empty one matches all.
type Matchers struct {
	Severities []notify.Severity
	Groups     []tenant.GroupID
	// Rules are patterns of rule IDs; * stands for any characters (security.*).
	Rules []string
	// Labels must all equal the rule's labels.
	Labels map[string]string
}

// Target is what matchers look at.
type Target struct {
	Rule     alert.RuleID
	Labels   map[string]string
	Group    tenant.GroupID
	Severity notify.Severity
}

// Match reports whether the matchers select the target.
func (m Matchers) Match(t Target) bool {
	if len(m.Severities) > 0 && !slices.Contains(m.Severities, t.Severity) {
		return false
	}
	if len(m.Groups) > 0 && !slices.Contains(m.Groups, t.Group) {
		return false
	}
	if len(m.Rules) > 0 && !slices.ContainsFunc(m.Rules, func(p string) bool {
		ok, err := path.Match(p, string(t.Rule))
		return err == nil && ok
	}) {
		return false
	}
	for k, v := range m.Labels {
		if got, ok := t.Labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// Escalation sends a firing alert to more channels when nobody acknowledged it in time.
type Escalation struct {
	After    time.Duration
	Channels []notify.ChannelID
}

// Policy routes the matching alerts of an organization to channels.
type Policy struct {
	ID       PolicyID
	Name     string
	Enabled  bool
	Match    Matchers
	Channels []notify.ChannelID
	Grouping Grouping
	// Repeat sends a firing, unacknowledged alert again after this time (0: never).
	Repeat     time.Duration
	Escalation *Escalation
	CreatedAt  time.Time
	UpdatedAt  time.Time
	CreatedBy  identity.UserID
}

// Validate checks the policy on its own (names, limits, patterns); whether its channels and
// groups exist is the business of the service. Messages name the field, never a value.
func (p *Policy) Validate() error {
	p.Name = strings.TrimSpace(p.Name)
	switch {
	case p.Name == "" || utf8.RuneCountInString(p.Name) > maxName || strings.IndexFunc(p.Name, unicode.IsControl) >= 0:
		return invalid("the name needs 1 to %d characters without control characters", maxName)
	case p.Grouping != GroupByNode && p.Grouping != GroupDigest:
		return invalid("grouping must be node or digest")
	case p.Repeat != 0 && (p.Repeat < MinRepeat || p.Repeat > MaxRepeat):
		return invalid("repeat must be 0 or between %v and %v", MinRepeat, MaxRepeat)
	case len(p.Channels) == 0 || len(p.Channels) > MaxChannels || hasDuplicates(p.Channels):
		return invalid("a policy needs 1 to %d different channels", MaxChannels)
	}
	if e := p.Escalation; e != nil {
		switch {
		case e.After < MinEscalation || e.After > MaxEscalation:
			return invalid("escalation must start between %v and %v", MinEscalation, MaxEscalation)
		case len(e.Channels) == 0 || len(e.Channels) > MaxChannels || hasDuplicates(e.Channels):
			return invalid("an escalation needs 1 to %d different channels", MaxChannels)
		}
	}
	return p.Match.validate()
}

func (m Matchers) validate() error {
	for _, s := range m.Severities {
		if s != notify.Warning && s != notify.Critical {
			return invalid("severities must be warning or critical")
		}
	}
	switch {
	case len(m.Severities) > 2 || hasDuplicates(m.Severities):
		return invalid("each severity at most once")
	case len(m.Groups) > MaxMatchValues || hasDuplicates(m.Groups):
		return invalid("at most %d different groups", MaxMatchValues)
	case len(m.Rules) > MaxMatchValues || hasDuplicates(m.Rules):
		return invalid("at most %d different rule patterns", MaxMatchValues)
	case len(m.Labels) > MaxLabels:
		return invalid("at most %d labels", MaxLabels)
	}
	for _, r := range m.Rules {
		if len(r) > maxPattern || !rulePattern.MatchString(r) {
			return invalid("rule patterns take lowercase letters, digits, _, . and *")
		}
	}
	for k, v := range m.Labels {
		if !labelKey.MatchString(k) || v == "" || len(v) > maxLabelValueLen || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return invalid("labels need a key like category and a value of 1 to %d bytes", maxLabelValueLen)
		}
	}
	return nil
}

func hasDuplicates[T comparable](values []T) bool {
	seen := make(map[T]bool, len(values))
	for _, v := range values {
		if seen[v] {
			return true
		}
		seen[v] = true
	}
	return false
}

// ErrInvalidPolicy means a policy is malformed.
var ErrInvalidPolicy = errors.New("invalid notification policy")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, fmt.Sprintf(format, args...))
}
