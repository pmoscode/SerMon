// Package alert is the domain of alerting (KONZEPT §12.1, M2-01): rules, thresholds with
// hysteresis and the state machine of an alert instance (rule × node × dimension). It is pure –
// the engine (app/alerting) evaluates rules against metrics and events, persists instances and
// hands the transitions to the notifiers.
package alert

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// ErrInvalidRule means a rule or one of its parts is malformed.
var ErrInvalidRule = errors.New("invalid alert rule")

// RuleID names a rule: <category>.<subject>.<condition>, e.g. system.cpu.usage_high.
type RuleID string

var ruleID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}(\.[a-z][a-z0-9_]{0,31}){2,3}$`)

// maxRuleID bounds the length of a rule ID.
const maxRuleID = 100

// ParseRuleID checks the naming scheme.
func ParseRuleID(s string) (RuleID, error) {
	if len(s) > maxRuleID || !ruleID.MatchString(s) {
		return "", fmt.Errorf("%w: id %q must be <category>.<subject>.<condition> (lowercase, digits, _)", ErrInvalidRule, s)
	}
	return RuleID(s), nil
}

// Type is the kind of a rule; users write no free queries (KONZEPT §12.2).
type Type int

// Rule types.
const (
	Threshold Type = iota + 1
	Rate
	Event
	Absence
	StateRule
	Prediction
	Anomaly
)

var typeNames = map[Type]string{
	Threshold: "threshold", Rate: "rate", Event: "event", Absence: "absence",
	StateRule: "state", Prediction: "prediction", Anomaly: "anomaly",
}

func (t Type) String() string {
	if n, ok := typeNames[t]; ok {
		return n
	}
	return "invalid"
}

// ParseType returns the rule type of its YAML name.
func ParseType(s string) (Type, error) {
	for t, n := range typeNames {
		if n == s {
			return t, nil
		}
	}
	return 0, fmt.Errorf("%w: unknown type %q", ErrInvalidRule, s)
}

// Flapping limits how often an instance may change between firing and resolved: more than
// Changes in Window set the flapping flag (notifications are throttled then).
type Flapping struct {
	Changes int
	Window  time.Duration
}

// DefaultFlapping is used when a rule sets nothing.
var DefaultFlapping = Flapping{Changes: 5, Window: 30 * time.Minute}

// maxFor bounds how long a condition may need to hold before an alert fires.
const maxFor = 24 * time.Hour

// Timing is how an instance reacts over time.
type Timing struct {
	// For is how long a condition must hold before the alert fires (0: at once). It applies when
	// leaving OK; a firing alert escalates to critical at once.
	For      time.Duration
	Flapping Flapping
}

// Validate checks the bounds.
func (t Timing) Validate() error {
	switch {
	case t.For < 0 || t.For > maxFor:
		return fmt.Errorf("%w: for must be between 0 and %v", ErrInvalidRule, maxFor)
	case t.Flapping.Changes < 1 || t.Flapping.Window <= 0:
		return fmt.Errorf("%w: flapping needs a number of changes and a window", ErrInvalidRule)
	}
	return nil
}

// Key identifies an instance within an organization: rule × node × dimension (e.g. the labels
// key of a mountpoint; empty for rules without dimensions).
type Key struct {
	Rule      RuleID
	Node      identity.NodeID
	Dimension string
}
