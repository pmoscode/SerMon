package anomaly

import (
	"math"
	"time"
)

// Point is one minute of a series.
type Point struct {
	T time.Time
	V float64
}

// Season is how far back the seasonal baseline looks (ADR 0037).
const Season = 4 * 7 * 24 * time.Hour

// Seasons holds the values of a series per hour of the week, for the seasonal baselines as the
// worker builds them: a bucket's baseline is computed from its values of the last Season before
// its hour begins.
type Seasons struct {
	buckets [168][]Point
}

// Add keeps p for its bucket.
func (s *Seasons) Add(p Point) {
	b := &s.buckets[HourOfWeek(p.T)]
	*b = append(*b, p)
}

// Baseline returns the baseline of the bucket of t from its values in [t − Season, start of t's
// hour); values before that are dropped.
func (s *Seasons) Baseline(t time.Time) (Baseline, bool) {
	hour := t.Truncate(time.Hour)
	b := &s.buckets[HourOfWeek(t)]
	from := hour.Add(-Season)
	kept := (*b)[:0]
	for _, p := range *b {
		if !p.T.Before(from) {
			kept = append(kept, p)
		}
	}
	*b = kept
	values := make([]float64, 0, len(kept))
	for _, p := range kept {
		if p.T.Before(hour) {
			values = append(values, p.V)
		}
	}
	return NewBaseline(values)
}

// Replay scores points (one per minute, oldest first) as the worker would: each against the
// seasonal baseline of its hour and the state before it.
func Replay(d Detector, points []Point) []Verdict {
	var seasons Seasons
	var state State
	out := make([]Verdict, len(points))
	var hour time.Time
	var baseline Baseline
	var has bool
	for i, p := range points {
		if h := p.T.Truncate(time.Hour); !h.Equal(hour) {
			hour = h
			baseline, has = seasons.Baseline(p.T)
		}
		out[i] = d.Score(&state, p.T, p.V, baseline, has)
		if !math.IsNaN(p.V) {
			seasons.Add(p)
		}
	}
	return out
}
