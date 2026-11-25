// Package anomaly detects unusual values of a metric without fixed thresholds (KONZEPT §13,
// ADR 0037): a seasonal baseline (median and MAD per hour of the week) and a short-term one
// (EWMA), anomalous only when both deviate by more than Threshold, debounced over five minutes.
// Pure: no I/O; the worker, the golden tests and the benchmark use the same code.
package anomaly

import (
	"math"
	"slices"
	"time"
)

// Parameters of the method (KONZEPT §13).
const (
	// Threshold is the deviation, in robust standard deviations, both baselines must exceed.
	Threshold = 4.0
	// Notable is the score from which a minute is stored for "what was unusual?" (ADR 0037).
	Notable = 3.0
	// HalfLife is the EWMA's half-life.
	HalfLife = time.Hour
	// MinPoints is how many values a seasonal bucket needs before it votes.
	MinPoints = 30
	// Warmup is how many values the EWMA needs before it votes.
	Warmup = 30
	// Gap resets the EWMA: after this long without values the old level says little.
	Gap = 6 * HalfLife
	// Window and Needed debounce: anomalous once Needed of the last Window minutes are.
	Window = 5
	Needed = 3
	// madScale makes the MAD comparable to a standard deviation of normal data.
	madScale = 1.4826
	// relativeFloor is the smallest spread relative to the level (1 %), for constant series.
	relativeFloor = 0.01
)

// Baseline is the seasonal baseline of one bucket: median and MAD of its values, and the usual
// range from the Lower to the Upper quantile.
type Baseline struct {
	Median, MAD float64
	Low, High   float64
	N           int
}

// The usual range of a bucket: what happens in at least 5 % of an hour (a nightly job of a few
// minutes) is usual at that hour.
const (
	lowerQuantile = 0.05
	upperQuantile = 0.95
)

// NewBaseline computes the baseline of values; NaN and infinite values are left out. ok is false
// with fewer than MinPoints values.
func NewBaseline(values []float64) (b Baseline, ok bool) {
	clean := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	if len(clean) < MinPoints {
		return Baseline{N: len(clean)}, false
	}
	median := medianOf(clean)
	low, high := quantile(clean, lowerQuantile), quantile(clean, upperQuantile)
	for i, v := range clean {
		clean[i] = math.Abs(v - median)
	}
	return Baseline{Median: median, MAD: medianOf(clean), Low: low, High: high, N: len(clean)}, true
}

// quantile of sorted values, interpolated linearly.
func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	i := int(pos)
	if i+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[i] + (pos-float64(i))*(sorted[i+1]-sorted[i])
}

// medianOf sorts values in place.
func medianOf(values []float64) float64 {
	slices.Sort(values)
	n := len(values)
	if n%2 == 1 {
		return values[n/2]
	}
	return (values[n/2-1] + values[n/2]) / 2
}

// Floor is the smallest spread at level: floor, at least relativeFloor of the level (constant
// series would otherwise turn any change into an infinite deviation).
func Floor(floor, level float64) float64 {
	return math.Max(floor, relativeFloor*math.Abs(level))
}

// Z is the robust z-score of x: |x − median| / (1.4826 · MAD), the spread at least
// Floor(floor, median).
func (b Baseline) Z(x, floor float64) float64 {
	outside := math.Max(0, math.Max(x-b.High, b.Low-x))
	return outside / math.Max(madScale*b.MAD, Floor(floor, b.Median))
}

// EWMA is the short-term baseline: exponentially weighted mean and variance.
type EWMA struct {
	Mean, Var float64
	N         int
	Last      time.Time
}

// Z is how far x lies from the mean in standard deviations (the spread at least floor); 0 while
// warming up.
func (e EWMA) Z(x, floor float64) float64 {
	if e.N < Warmup {
		return 0
	}
	return math.Abs(x-e.Mean) / math.Max(math.Sqrt(e.Var), floor)
}

// Update adds x at at; the weight of the past halves every HalfLife. After a Gap it starts anew.
//
// Robust against what it should detect: once warmed up, a value more than Threshold spreads
// (at least floor) away moves the mean only by Threshold spreads and leaves the variance as it
// is. Otherwise a spike would widen the variance within minutes and hide itself; a lasting
// change of the level is still absorbed, over hours.
func (e *EWMA) Update(at time.Time, x, floor float64) {
	if e.N == 0 || at.Sub(e.Last) > Gap {
		*e = EWMA{Mean: x, N: 1, Last: at}
		return
	}
	dt := at.Sub(e.Last)
	if dt <= 0 {
		return
	}
	alpha := 1 - math.Exp2(-dt.Seconds()/HalfLife.Seconds())
	diff := x - e.Mean
	limit := Threshold * math.Max(math.Sqrt(e.Var), floor)
	if e.N >= Warmup && math.Abs(diff) > limit {
		e.Mean += alpha * math.Copysign(limit, diff)
	} else {
		e.Mean += alpha * diff
		e.Var = (1 - alpha) * (e.Var + alpha*diff*diff)
	}
	e.N++
	e.Last = at
}

// Debouncer remembers the raw verdicts of the last Window minutes.
type Debouncer struct {
	bits uint8
}

// Push adds a raw verdict and reports whether Needed of the last Window are anomalous.
func (d *Debouncer) Push(raw bool) bool {
	d.bits <<= 1
	if raw {
		d.bits |= 1
	}
	d.bits &= 1<<Window - 1
	count := 0
	for b := d.bits; b != 0; b &= b - 1 {
		count++
	}
	return count >= Needed
}

// State is what a detector keeps per series between minutes.
type State struct {
	EWMA      EWMA
	Debouncer Debouncer
	// Residual: the EWMA follows the deviation from the seasonal median (else the value).
	Residual bool
}

// Verdict is the result of one minute.
type Verdict struct {
	// Seasonal and Short are the z-scores against the two baselines (0 where one doesn't vote).
	Seasonal, Short float64
	// Score is the smaller of both: how unusual the value is by both (for sorting and the band).
	Score float64
	// Raw: both exceed Threshold now; Anomalous: after debouncing.
	Raw, Anomalous bool
	// Expected is the seasonal median (NaN without a baseline), for "5× higher than usual".
	Expected float64
}

// Notable reports whether the minute is worth storing (ADR 0037).
func (v Verdict) Notable() bool { return v.Anomalous || v.Score >= Notable }

// Detector scores a value of a series; the interface allows further methods (v2: isolation
// forest, k-means) behind the same worker.
type Detector interface {
	Score(s *State, at time.Time, x float64, seasonal Baseline, hasSeasonal bool) Verdict
}

// Robust is the method of KONZEPT §13. Floor is the series' smallest meaningful spread in its
// unit (e.g. 0.1 % for a ratio), against constant series.
type Robust struct {
	Floor float64
}

// Score scores x against the state before x, then adds x to it.
//
// The short-term baseline follows the deviation from the seasonal median while there is one: a
// mean of the values themselves lags behind a day cycle, and its variance would measure the lag
// instead of the noise. Without a seasonal baseline (learning) it follows the values; a change of
// basis starts it anew.
func (r Robust) Score(s *State, at time.Time, x float64, seasonal Baseline, hasSeasonal bool) Verdict {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return Verdict{Expected: math.NaN()}
	}
	v := Verdict{Expected: math.NaN()}
	if s.Residual != hasSeasonal {
		s.EWMA, s.Residual = EWMA{}, hasSeasonal
	}
	basis, level := x, s.EWMA.Mean
	if hasSeasonal {
		basis, level = x-seasonal.Median, seasonal.Median
		v.Seasonal, v.Expected = seasonal.Z(x, r.Floor), seasonal.Median
	}
	floor := Floor(r.Floor, level)
	if at.Sub(s.EWMA.Last) <= Gap {
		v.Short = s.EWMA.Z(basis, floor)
	}
	v.Score = math.Min(v.Seasonal, v.Short)
	v.Raw = v.Seasonal > Threshold && v.Short > Threshold
	v.Anomalous = s.Debouncer.Push(v.Raw)
	s.EWMA.Update(at, basis, floor)
	return v
}

// HourOfWeek is the seasonal bucket of t: 0 (Monday 00:00 UTC) to 167 (ADR 0037).
func HourOfWeek(t time.Time) int {
	t = t.UTC()
	return (int(t.Weekday())+6)%7*24 + t.Hour()
}
