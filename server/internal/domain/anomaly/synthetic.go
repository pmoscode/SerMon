package anomaly

import (
	"math"
	"math/rand/v2"
	"time"
)

// Synthetic series for the golden tests, the benchmark and test histories (KONZEPT §13):
// noise, daily and weekly seasonality, spikes, level shifts, drifts and gaps, reproducible
// from a seed.

// Spec describes a synthetic series of one value per minute.
type Spec struct {
	Start time.Time
	Weeks int
	// Level is the mean, Noise the standard deviation of the normal noise; Counts makes values
	// whole and not negative (process counts, connections).
	Level, Noise float64
	Counts       bool
	// Daily is the amplitude of a day cycle peaking at 14:00 UTC; Weekend lowers Saturdays and
	// Sundays by that much.
	Daily, Weekend float64
	// Routines repeat every day at At (from midnight UTC), like a nightly job: learnt, no events.
	Routines []Change
	// Outliers is the probability of a single minute 8 noise away (heavy tails).
	Outliers float64
	Spikes   []Change
	Shifts   []Change
	Drifts   []Change
	Gaps     []Change
}

// Change is a deviation from the pattern: Size in units of Noise (spikes, shifts, drifts at
// their end) from At for Duration (a shift lasts to the end).
type Change struct {
	At       time.Duration // from Start
	Duration time.Duration
	Size     float64
}

// Event is a labelled deviation: a detector must flag a minute in [Start, Due) to find it
// (Required) and may flag minutes up to Allowed without a false positive.
type Event struct {
	Start, Due, Allowed time.Time
	Required            bool
}

// Synthetic is a generated series with its events.
type Synthetic struct {
	Points []Point
	Events []Event
}

// Generate builds the series of spec from seed.
func Generate(seed uint64, spec Spec) Synthetic {
	r := rand.New(rand.NewPCG(seed, seed^0x5e4d0a)) //nolint:gosec // test data, not security
	minutes := spec.Weeks * 7 * 24 * 60
	out := Synthetic{Points: make([]Point, 0, minutes)}
	within := func(c Change, at time.Duration) bool { return at >= c.At && at < c.At+c.Duration }
	for i := range minutes {
		at := time.Duration(i) * time.Minute
		t := spec.Start.Add(at)
		v := spec.Level + spec.Noise*r.NormFloat64()
		u := t.UTC()
		day := float64(u.Hour()*60+u.Minute()) / 1440
		v += spec.Daily * math.Cos(2*math.Pi*(day-14.0/24))
		if wd := u.Weekday(); wd == time.Saturday || wd == time.Sunday {
			v -= spec.Weekend
		}
		for _, c := range spec.Spikes {
			if within(c, at) {
				v += c.Size * spec.Noise
			}
		}
		for _, c := range spec.Routines {
			if within(c, at%(24*time.Hour)) {
				v += c.Size * spec.Noise
			}
		}
		if r.Float64() < spec.Outliers {
			v += math.Copysign(8*spec.Noise, r.NormFloat64())
		}
		for _, c := range spec.Shifts {
			if at >= c.At {
				v += c.Size * spec.Noise
			}
		}
		for _, c := range spec.Drifts {
			switch {
			case within(c, at):
				v += c.Size * spec.Noise * float64(at-c.At) / float64(c.Duration)
			case at >= c.At+c.Duration:
				v += c.Size * spec.Noise
			}
		}
		gap := false
		for _, c := range spec.Gaps {
			gap = gap || within(c, at)
		}
		if gap {
			continue
		}
		if spec.Counts {
			v = math.Max(0, math.Round(v))
		}
		out.Points = append(out.Points, Point{T: t, V: v})
	}
	at := func(d time.Duration) time.Time { return spec.Start.Add(d) }
	debounce := Window * time.Minute
	for _, c := range spec.Spikes {
		out.Events = append(out.Events, Event{
			Start: at(c.At), Due: at(c.At + c.Duration + debounce), Allowed: at(c.At + c.Duration + 2*debounce),
			Required: c.Duration >= Needed*time.Minute,
		})
	}
	for _, c := range spec.Shifts {
		// Found within the hour; flagged for a few hours until the short-term baseline follows.
		out.Events = append(out.Events, Event{Start: at(c.At), Due: at(c.At + time.Hour), Allowed: at(c.At + 8*HalfLife), Required: true})
	}
	for _, c := range spec.Drifts {
		// A slow change needn't be found; flagging it is no mistake.
		out.Events = append(out.Events, Event{Start: at(c.At), Due: at(c.At), Allowed: at(c.At + c.Duration + 8*HalfLife)})
	}
	for _, c := range spec.Gaps {
		// After a gap the short-term baseline starts anew: nothing to find, nothing to flag.
		out.Events = append(out.Events, Event{Start: at(c.At + c.Duration), Due: at(c.At + c.Duration), Allowed: at(c.At + c.Duration)})
	}
	return out
}
