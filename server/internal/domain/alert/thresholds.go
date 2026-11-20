package alert

import (
	"errors"
	"fmt"
	"math"
)

// ErrInvalidThresholds means warning, critical and clear contradict each other.
var ErrInvalidThresholds = errors.New("invalid alert thresholds")

// Level is the severity a value reaches.
type Level int

// Levels.
const (
	LevelOK Level = iota
	LevelWarning
	LevelCritical
)

// Op compares a value with a threshold.
type Op string

// Operators. Above fires on rising values (CPU), Below on falling ones (available memory).
const (
	Above        Op = ">"
	AboveOrEqual Op = ">="
	Below        Op = "<"
	BelowOrEqual Op = "<="
)

func (o Op) rising() bool  { return o == Above || o == AboveOrEqual }
func (o Op) falling() bool { return o == Below || o == BelowOrEqual }

// Condition is a comparison with a threshold.
type Condition struct {
	Op    Op
	Value float64
}

// Holds reports whether v meets the condition.
func (c Condition) Holds(v float64) bool {
	switch c.Op {
	case Above:
		return v > c.Value
	case AboveOrEqual:
		return v >= c.Value
	case Below:
		return v < c.Value
	case BelowOrEqual:
		return v <= c.Value
	}
	return false
}

// Thresholds are the conditions of a rule. Clear is the hysteresis: a firing alert resolves only
// once the value meets it; without Clear it resolves as soon as no level is reached.
type Thresholds struct {
	Warning, Critical, Clear *Condition
}

// Validate rejects contradictions: both levels in the same direction, critical beyond warning,
// clear in the opposite direction and not beyond the first firing threshold.
func (t Thresholds) Validate() error {
	if t.Warning == nil && t.Critical == nil {
		return fmt.Errorf("%w: warning or critical is required", ErrInvalidThresholds)
	}
	for _, c := range []*Condition{t.Warning, t.Critical, t.Clear} {
		if c != nil && (!c.Op.rising() && !c.Op.falling() || math.IsNaN(c.Value) || math.IsInf(c.Value, 0)) {
			return fmt.Errorf("%w: operator must be >, >=, < or <= with a finite value", ErrInvalidThresholds)
		}
	}
	first := t.Warning
	if first == nil {
		first = t.Critical
	}
	rising := first.Op.rising()
	if t.Warning != nil && t.Critical != nil {
		if t.Critical.Op.rising() != rising {
			return fmt.Errorf("%w: warning and critical must point in the same direction", ErrInvalidThresholds)
		}
		if (rising && t.Critical.Value < t.Warning.Value) || (!rising && t.Critical.Value > t.Warning.Value) {
			return fmt.Errorf("%w: critical must lie beyond warning", ErrInvalidThresholds)
		}
	}
	if c := t.Clear; c != nil {
		if c.Op.rising() == rising {
			return fmt.Errorf("%w: clear must point in the opposite direction", ErrInvalidThresholds)
		}
		if (rising && c.Value > first.Value) || (!rising && c.Value < first.Value) {
			return fmt.Errorf("%w: clear must not lie beyond the first firing threshold", ErrInvalidThresholds)
		}
	}
	return nil
}

// Observation is what one evaluation found.
type Observation struct {
	Level   Level
	Cleared bool // the value meets the clear condition (a firing alert resolves)
	Value   float64
	NoData  bool // nothing to evaluate: the instance keeps its state
}

// Evaluate classifies a value; NaN means no data.
func (t Thresholds) Evaluate(v float64) Observation {
	if math.IsNaN(v) {
		return Observation{NoData: true}
	}
	o := Observation{Value: v}
	switch {
	case t.Critical != nil && t.Critical.Holds(v):
		o.Level = LevelCritical
	case t.Warning != nil && t.Warning.Holds(v):
		o.Level = LevelWarning
	}
	if t.Clear != nil {
		o.Cleared = t.Clear.Holds(v)
	} else {
		o.Cleared = o.Level == LevelOK
	}
	return o
}
