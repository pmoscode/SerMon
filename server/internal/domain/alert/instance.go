package alert

import (
	"errors"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// ErrNotFiring means only a firing alert can be acknowledged.
var ErrNotFiring = errors.New("the alert is not firing")

// State is where an instance is in its life cycle.
type State int

// States: OK → PENDING → WARNING | CRITICAL → RESOLVED → OK (KONZEPT §12.1). Acknowledged and
// flapping are flags, silences belong to the routing.
const (
	StateOK State = iota
	StatePending
	StateWarning
	StateCritical
	StateResolved
)

var stateNames = map[State]string{
	StateOK: "ok", StatePending: "pending", StateWarning: "warning", StateCritical: "critical", StateResolved: "resolved",
}

func (s State) String() string {
	if n, ok := stateNames[s]; ok {
		return n
	}
	return "invalid"
}

// Firing reports whether the state notifies (warning or critical).
func (s State) Firing() bool { return s == StateWarning || s == StateCritical }

var allowed = map[State][]State{
	StateOK:       {StatePending, StateWarning, StateCritical},
	StatePending:  {StateOK, StateWarning, StateCritical},
	StateWarning:  {StateCritical, StateResolved},
	StateCritical: {StateWarning, StateResolved},
	StateResolved: {StateOK, StatePending, StateWarning, StateCritical},
}

// CanBecome reports whether the state machine allows s → next.
func (s State) CanBecome(next State) bool {
	for _, n := range allowed[s] {
		if n == next {
			return true
		}
	}
	return false
}

func firingState(l Level) State {
	if l == LevelCritical {
		return StateCritical
	}
	return StateWarning
}

// TransitionKind says what happened.
type TransitionKind int

// Kinds of transitions: the engine stores them as history and routes them to the notifiers.
const (
	StateChanged TransitionKind = iota + 1
	Acknowledged
	FlappingStarted
	FlappingStopped
)

// Transition is one change of an instance.
type Transition struct {
	Kind     TransitionKind
	From, To State
	At       time.Time
	Value    float64
	By       identity.UserID // Acknowledged
	Fields   Fields          // of the observation that caused it, or the instance's
}

// Ack records who acknowledged a firing alert.
type Ack struct {
	By identity.UserID
	At time.Time
}

// Instance is the state of a rule for one node (and dimension).
type Instance struct {
	Key          Key
	State        State
	Since        time.Time // when State began
	PendingSince time.Time // set while pending
	FiredAt      time.Time // set while firing
	Ack          *Ack      // set while firing and acknowledged
	Flapping     bool
	Value        float64     // of the latest observation
	Fields       Fields      // of the observation behind the last state change
	Flips        []time.Time // recent changes between firing and resolved, newest last
}

// NewInstance returns an instance in OK.
func NewInstance(key Key, at time.Time) *Instance {
	return &Instance{Key: key, State: StateOK, Since: at}
}

// Observe applies an evaluation at time at and returns what changed. Observations without data
// change nothing (missing data is the business of absence rules).
func (i *Instance) Observe(o Observation, at time.Time, t Timing) []Transition {
	if o.NoData {
		return nil
	}
	i.Value = o.Value
	var out []Transition
	move := func(to State) {
		out = append(out, Transition{Kind: StateChanged, From: i.State, To: to, At: at, Value: o.Value, Fields: o.Fields})
		i.Fields = o.Fields
		wasFiring := i.State.Firing()
		if to.Firing() && !wasFiring || to == StateResolved {
			i.Flips = append(i.Flips, at)
		}
		if to == StateCritical && i.State == StateWarning {
			i.Ack = nil // escalation must not hide behind an old acknowledgement
		}
		i.State, i.Since = to, at
		switch {
		case to == StatePending:
			i.PendingSince = at
		case to.Firing():
			i.PendingSince = time.Time{}
			if !wasFiring {
				i.FiredAt = at
			}
		default:
			i.PendingSince, i.FiredAt, i.Ack = time.Time{}, time.Time{}, nil
		}
	}
	fire := func() {
		if t.For == 0 {
			move(firingState(o.Level))
		} else {
			move(StatePending)
		}
	}

	switch i.State {
	case StateOK:
		if o.Level >= LevelWarning {
			fire()
		}
	case StatePending:
		switch {
		case o.Level == LevelOK:
			move(StateOK)
		case at.Sub(i.PendingSince) >= t.For:
			move(firingState(o.Level))
		}
	case StateWarning, StateCritical:
		if o.Cleared {
			move(StateResolved)
		} else if next := firingState(o.Level); next != i.State {
			// Between the firing threshold and clear the alert holds at warning (hysteresis).
			move(next)
		}
	case StateResolved:
		if o.Level >= LevelWarning {
			fire()
		} else {
			move(StateOK)
		}
	}
	return append(out, i.updateFlapping(at, t.Flapping)...)
}

func (i *Instance) updateFlapping(at time.Time, f Flapping) []Transition {
	if f.Changes < 1 || f.Window <= 0 {
		return nil
	}
	recent := i.Flips[:0]
	for _, flip := range i.Flips {
		if at.Sub(flip) < f.Window {
			recent = append(recent, flip)
		}
	}
	if len(recent) > f.Changes+1 {
		recent = recent[len(recent)-f.Changes-1:]
	}
	i.Flips = recent
	flapping := len(recent) > f.Changes
	if flapping == i.Flapping {
		return nil
	}
	i.Flapping = flapping
	kind := FlappingStopped
	if flapping {
		kind = FlappingStarted
	}
	return []Transition{{Kind: kind, From: i.State, To: i.State, At: at, Value: i.Value, Fields: i.Fields}}
}

// Retire ends the instance because its rule no longer applies to it (disabled, removed, node
// outside the rule's groups): a firing alert resolves, a pending one falls back to OK. The engine
// deletes the instance afterwards.
func (i *Instance) Retire(at time.Time) []Transition {
	var to State
	switch {
	case i.State.Firing():
		to = StateResolved
	case i.State == StatePending:
		to = StateOK
	default:
		return nil
	}
	t := Transition{Kind: StateChanged, From: i.State, To: to, At: at, Value: i.Value, Fields: i.Fields}
	i.State, i.Since = to, at
	i.PendingSince, i.FiredAt, i.Ack = time.Time{}, time.Time{}, nil
	return []Transition{t}
}

// Acknowledge marks a firing alert as seen; notifications stop until it escalates or resolves.
func (i *Instance) Acknowledge(by identity.UserID, at time.Time) (Transition, error) {
	if !i.State.Firing() {
		return Transition{}, ErrNotFiring
	}
	i.Ack = &Ack{By: by, At: at}
	return Transition{Kind: Acknowledged, From: i.State, To: i.State, At: at, Value: i.Value, By: by, Fields: i.Fields}, nil
}

// Check returns a description of the first broken invariant, "" if the instance is consistent
// (tests, and the engine after loading persisted instances).
func (i *Instance) Check() string {
	switch {
	case stateNames[i.State] == "":
		return "unknown state"
	case i.Since.IsZero():
		return "state without a start time"
	case (i.State == StatePending) != !i.PendingSince.IsZero():
		return "pending time set outside pending"
	case i.State.Firing() != !i.FiredAt.IsZero():
		return "fire time set outside firing"
	case i.Ack != nil && !i.State.Firing():
		return "acknowledged without firing"
	}
	return ""
}
