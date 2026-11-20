package alert_test

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func gt(v float64) *alert.Condition { return &alert.Condition{Op: alert.Above, Value: v} }
func lt(v float64) *alert.Condition { return &alert.Condition{Op: alert.Below, Value: v} }

// cpu is the CPU rule of KONZEPT §12.2: warning > 85 %, critical > 95 %, clear < 80 %.
var cpu = alert.Thresholds{Warning: gt(0.85), Critical: gt(0.95), Clear: lt(0.80)}

func TestThresholds_EvaluateLevelAndClear(t *testing.T) {
	memoryLow := alert.Thresholds{Warning: lt(0.20), Critical: lt(0.10), Clear: gt(0.25)} // available memory
	for name, tc := range map[string]struct {
		th      alert.Thresholds
		value   float64
		level   alert.Level
		cleared bool
	}{
		"below clear":              {cpu, 0.50, alert.LevelOK, true},
		"between clear and warn":   {cpu, 0.82, alert.LevelOK, false},
		"exactly on the warning":   {cpu, 0.85, alert.LevelOK, false},
		"warning":                  {cpu, 0.90, alert.LevelWarning, false},
		"critical":                 {cpu, 0.99, alert.LevelCritical, false},
		"falling metric, ok":       {memoryLow, 0.50, alert.LevelOK, true},
		"falling metric, critical": {memoryLow, 0.05, alert.LevelCritical, false},
		"without clear: ok clears": {alert.Thresholds{Warning: gt(0.85)}, 0.84, alert.LevelOK, true},
		"only critical":            {alert.Thresholds{Critical: gt(0.95)}, 0.96, alert.LevelCritical, false},
	} {
		o := tc.th.Evaluate(tc.value)
		if o.Level != tc.level || o.Cleared != tc.cleared || o.NoData || o.Value != tc.value {
			t.Errorf("%s: Evaluate(%v) = %+v, want level %v cleared %v", name, tc.value, o, tc.level, tc.cleared)
		}
	}
	if o := cpu.Evaluate(math.NaN()); !o.NoData {
		t.Errorf("Evaluate(NaN) = %+v, want no data", o)
	}
}

func TestThresholds_RejectContradictions(t *testing.T) {
	if err := cpu.Validate(); err != nil {
		t.Fatalf("cpu thresholds: %v", err)
	}
	for name, th := range map[string]alert.Thresholds{
		"nothing to fire on":          {Clear: lt(0.8)},
		"critical below warning":      {Warning: gt(0.9), Critical: gt(0.8)},
		"mixed directions":            {Warning: gt(0.8), Critical: lt(0.1)},
		"clear in the same direction": {Warning: gt(0.8), Clear: gt(0.7)},
		"clear above the warning":     {Warning: gt(0.8), Clear: lt(0.9)},
		"not a number":                {Warning: gt(math.NaN())},
		"unknown operator":            {Warning: &alert.Condition{Op: "==", Value: 1}},
	} {
		if err := th.Validate(); !errors.Is(err, alert.ErrInvalidThresholds) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidThresholds", name, err)
		}
	}
}

type step struct {
	after time.Duration // since the previous step
	value float64
	state alert.State
	moves []alert.State // states entered in this step, in order
}

func run(t *testing.T, timing alert.Timing, steps []step) *alert.Instance {
	t.Helper()
	inst := alert.NewInstance(alert.Key{Rule: "system.cpu.usage_high", Node: identity.NodeID(uuid.New())}, t0)
	at := t0
	for i, s := range steps {
		at = at.Add(s.after)
		var moves []alert.State
		for _, tr := range inst.Observe(cpu.Evaluate(s.value), at, timing) {
			if tr.Kind == alert.StateChanged {
				moves = append(moves, tr.To)
				if tr.At != at || tr.Value != s.value {
					t.Errorf("step %d: transition %+v, want at %v with value %v", i, tr, at, s.value)
				}
			}
		}
		if inst.State != s.state || !equal(moves, s.moves) {
			t.Fatalf("step %d (+%v, %v): state %v, moves %v; want %v, %v", i, s.after, s.value, inst.State, moves, s.state, s.moves)
		}
	}
	return inst
}

func equal(a, b []alert.State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var fiveMinutes = alert.Timing{For: 5 * time.Minute, Flapping: alert.DefaultFlapping}

func TestObserve_FiresOnlyAfterForAndResolvesBelowClear(t *testing.T) {
	inst := run(t, fiveMinutes, []step{
		{0, 0.50, alert.StateOK, nil},
		{10 * time.Second, 0.90, alert.StatePending, []alert.State{alert.StatePending}},
		{4 * time.Minute, 0.91, alert.StatePending, nil},
		{time.Minute, 0.92, alert.StateWarning, []alert.State{alert.StateWarning}},
		// Hysteresis: below the warning but above clear stays firing.
		{time.Minute, 0.82, alert.StateWarning, nil},
		{time.Minute, 0.79, alert.StateResolved, []alert.State{alert.StateResolved}},
		{10 * time.Second, 0.50, alert.StateOK, []alert.State{alert.StateOK}},
	})
	if !inst.FiredAt.IsZero() || !inst.PendingSince.IsZero() {
		t.Errorf("after OK: fired %v, pending %v; want both cleared", inst.FiredAt, inst.PendingSince)
	}
}

func TestObserve_PendingFallsBackWithoutFiring(t *testing.T) {
	run(t, fiveMinutes, []step{
		{0, 0.90, alert.StatePending, []alert.State{alert.StatePending}},
		{2 * time.Minute, 0.70, alert.StateOK, []alert.State{alert.StateOK}},
		// A new excursion starts its own pending time.
		{time.Minute, 0.90, alert.StatePending, []alert.State{alert.StatePending}},
		{4 * time.Minute, 0.90, alert.StatePending, nil},
		{time.Minute, 0.99, alert.StateCritical, []alert.State{alert.StateCritical}},
	})
}

func TestObserve_EscalatesAtOnceAndDeescalatesWhileHeld(t *testing.T) {
	run(t, alert.Timing{Flapping: alert.DefaultFlapping}, []step{
		{0, 0.90, alert.StateWarning, []alert.State{alert.StateWarning}}, // for: 0 fires at once
		{10 * time.Second, 0.99, alert.StateCritical, []alert.State{alert.StateCritical}},
		{10 * time.Second, 0.90, alert.StateWarning, []alert.State{alert.StateWarning}},
		{10 * time.Second, 0.83, alert.StateWarning, nil},
		{10 * time.Second, 0.10, alert.StateResolved, []alert.State{alert.StateResolved}},
		// Firing again right after the resolution starts over (here: at once, for is 0).
		{10 * time.Second, 0.97, alert.StateCritical, []alert.State{alert.StateCritical}},
	})
}

func TestObserve_ResolvedGoesBackToPendingWithFor(t *testing.T) {
	run(t, fiveMinutes, []step{
		{0, 0.90, alert.StatePending, []alert.State{alert.StatePending}},
		{5 * time.Minute, 0.90, alert.StateWarning, []alert.State{alert.StateWarning}},
		{time.Minute, 0.10, alert.StateResolved, []alert.State{alert.StateResolved}},
		{10 * time.Second, 0.90, alert.StatePending, []alert.State{alert.StatePending}},
	})
}

func TestObserve_IgnoresMissingData(t *testing.T) {
	inst := run(t, fiveMinutes, []step{
		{0, 0.90, alert.StatePending, []alert.State{alert.StatePending}},
		{5 * time.Minute, 0.90, alert.StateWarning, []alert.State{alert.StateWarning}},
	})
	if moves := inst.Observe(alert.Observation{NoData: true}, t0.Add(time.Hour), fiveMinutes); len(moves) != 0 || inst.State != alert.StateWarning {
		t.Errorf("no data: %+v, state %v; want nothing to change (absence rules handle missing data)", moves, inst.State)
	}
}

func TestAcknowledge(t *testing.T) {
	ann := identity.UserID(uuid.New())
	inst := run(t, alert.Timing{}, []step{{0, 0.50, alert.StateOK, nil}})
	if _, err := inst.Acknowledge(ann, t0); !errors.Is(err, alert.ErrNotFiring) {
		t.Errorf("Acknowledge() while OK = %v, want ErrNotFiring", err)
	}

	inst.Observe(cpu.Evaluate(0.90), t0.Add(time.Minute), alert.Timing{})
	tr, err := inst.Acknowledge(ann, t0.Add(2*time.Minute))
	if err != nil || inst.Ack == nil || inst.Ack.By != ann || tr.Kind != alert.Acknowledged {
		t.Fatalf("Acknowledge() = %+v, %v; ack %+v", tr, err, inst.Ack)
	}
	// A critical escalation must not hide behind an old acknowledgement.
	inst.Observe(cpu.Evaluate(0.99), t0.Add(3*time.Minute), alert.Timing{})
	if inst.Ack != nil {
		t.Error("escalation to critical must clear the acknowledgement")
	}
	if _, err := inst.Acknowledge(ann, t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	inst.Observe(cpu.Evaluate(0.90), t0.Add(5*time.Minute), alert.Timing{})
	if inst.Ack == nil {
		t.Error("de-escalation keeps the acknowledgement")
	}
	inst.Observe(cpu.Evaluate(0.10), t0.Add(6*time.Minute), alert.Timing{})
	if inst.Ack != nil {
		t.Error("resolution ends the acknowledgement")
	}
}

// More than Changes alternations between firing and resolved within Window set the flag; the
// routing (M2-14) throttles notifications then. It ends once the window calms down.
func TestObserve_DetectsFlapping(t *testing.T) {
	timing := alert.Timing{Flapping: alert.Flapping{Changes: 4, Window: 10 * time.Minute}}
	inst := alert.NewInstance(alert.Key{Rule: "system.cpu.usage_high"}, t0)
	at := t0
	var started, stopped int
	observe := func(v float64) {
		at = at.Add(30 * time.Second)
		for _, tr := range inst.Observe(cpu.Evaluate(v), at, timing) {
			switch tr.Kind {
			case alert.FlappingStarted:
				started++
			case alert.FlappingStopped:
				stopped++
			case alert.StateChanged, alert.Acknowledged:
			}
		}
	}
	for range 2 {
		observe(0.90) // fire
		observe(0.10) // resolve
	}
	if inst.Flapping {
		t.Fatal("four changes are not more than four")
	}
	observe(0.90)
	if !inst.Flapping || started != 1 {
		t.Fatalf("fifth change: flapping %v, started %d", inst.Flapping, started)
	}
	observe(0.10)
	observe(0.50) // resolved → OK is no flip
	for range 20 {
		observe(0.50)
	}
	if inst.Flapping || stopped != 1 {
		t.Errorf("after a calm window: flapping %v, stopped %d", inst.Flapping, stopped)
	}
	if len(inst.Flips) > timing.Flapping.Changes+1 {
		t.Errorf("%d flips kept, want at most %d (bounded memory)", len(inst.Flips), timing.Flapping.Changes+1)
	}
}

// Any sequence of values and times leaves the instance consistent (property).
func TestObserve_InvariantsHoldForRandomSequences(t *testing.T) {
	ann := identity.UserID(uuid.New())
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 1))
		timing := alert.Timing{For: time.Duration(rng.IntN(4)) * time.Minute, Flapping: alert.Flapping{Changes: 1 + rng.IntN(5), Window: 15 * time.Minute}}
		inst := alert.NewInstance(alert.Key{Rule: "system.cpu.usage_high"}, t0)
		at := t0
		for i := range 300 {
			at = at.Add(time.Duration(1+rng.IntN(120)) * time.Second)
			o := cpu.Evaluate(rng.Float64())
			if rng.IntN(20) == 0 {
				o = alert.Observation{NoData: true}
			}
			before := inst.State
			for _, tr := range inst.Observe(o, at, timing) {
				if tr.Kind == alert.StateChanged && (tr.From != before || !tr.From.CanBecome(tr.To)) {
					t.Fatalf("seed %d step %d: invalid transition %v → %v", seed, i, tr.From, tr.To)
				}
				if tr.Kind == alert.StateChanged {
					before = tr.To
				}
			}
			if rng.IntN(10) == 0 {
				_, _ = inst.Acknowledge(ann, at)
			}
			if msg := inst.Check(); msg != "" {
				t.Fatalf("seed %d step %d: %s (%+v)", seed, i, msg, inst)
			}
		}
	}
}
