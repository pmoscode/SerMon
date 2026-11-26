package anomaly_test

import (
	"math"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/anomaly"
)

var t0 = time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC) // a Monday

func values(n int, f func(i int) float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestNewBaseline_MedianMADAndTheUsualRange(t *testing.T) {
	// 1..41: median 21, MAD 10, P5 3, P95 39.
	b, ok := anomaly.NewBaseline(values(41, func(i int) float64 { return float64(i + 1) }))
	if !ok || b.Median != 21 || b.MAD != 10 || !near(b.Low, 3) || !near(b.High, 39) || b.N != 41 {
		t.Fatalf("baseline = %+v, %v", b, ok)
	}
	// Even count, NaN and infinities left out.
	in := append(values(40, func(i int) float64 { return float64(i + 1) }), math.NaN(), math.Inf(1))
	b, ok = anomaly.NewBaseline(in)
	if !ok || b.Median != 20.5 || b.N != 40 {
		t.Errorf("baseline = %+v, %v", b, ok)
	}
	if _, ok := anomaly.NewBaseline(values(anomaly.MinPoints-1, func(int) float64 { return 1 })); ok {
		t.Error("too few points vote")
	}
	if _, ok := anomaly.NewBaseline(values(anomaly.MinPoints, func(int) float64 { return 1 })); !ok {
		t.Error("enough points don't vote")
	}
}

// The deviation counts from the usual range, in robust standard deviations; constant values get
// the floor.
func TestBaseline_ZFromTheUsualRange(t *testing.T) {
	b := anomaly.Baseline{Median: 100, MAD: 10, Low: 80, High: 120, N: 100}
	sigma := 1.4826 * 10
	for x, want := range map[float64]float64{100: 0, 120: 0, 80: 0, 120 + 4*sigma: 4, 80 - 2*sigma: 2} {
		if got := b.Z(x, 0); !near(got, want) {
			t.Errorf("Z(%v) = %v, want %v", x, got, want)
		}
	}
	constant := anomaly.Baseline{Median: 8, Low: 8, High: 8, N: 50}
	// 1 % of the level: 0.08.
	if got := constant.Z(8.4, 0); !near(got, 5) {
		t.Errorf("constant Z = %v", got)
	}
	if got := constant.Z(8.4, 0.4); !near(got, 1) {
		t.Errorf("with a floor Z = %v", got)
	}
	if got := anomaly.Floor(0, -50); got != 0.5 {
		t.Errorf("Floor = %v", got)
	}
}

func TestEWMA_HalfLifeWarmupGapsAndRobustness(t *testing.T) {
	var e anomaly.EWMA
	e.Update(t0, 0, 0)
	for i := 1; i <= 60; i++ {
		e.Update(t0.Add(time.Duration(i)*time.Minute), 10, 1e9)
	}
	// After one half-life of 10 the old 0 weighs half.
	if math.Abs(e.Mean-5) > 0.05 {
		t.Errorf("mean after a half-life = %v", e.Mean)
	}
	// The same time or earlier changes nothing; a gap starts anew.
	before := e
	e.Update(e.Last, 99, 0)
	e.Update(e.Last.Add(-time.Minute), 99, 0)
	if e != before {
		t.Errorf("an old value was taken: %+v", e)
	}
	e.Update(e.Last.Add(anomaly.Gap+time.Minute), 3, 0)
	if e.Mean != 3 || e.N != 1 || e.Var != 0 {
		t.Errorf("after a gap = %+v", e)
	}
	// Warming up: no vote.
	if e.Z(1000, 1) != 0 {
		t.Error("a cold EWMA votes")
	}
	// Warm: a value 10 spreads away moves the mean by 4 spreads only and keeps the variance.
	var w anomaly.EWMA
	for i := range anomaly.Warmup + 10 {
		w.Update(t0.Add(time.Duration(i)*time.Minute), float64(i%2), 0)
	}
	sd := math.Sqrt(w.Var)
	mean, variance := w.Mean, w.Var
	w.Update(w.Last.Add(time.Minute), mean+10*sd, 0)
	alpha := 1 - math.Exp2(-1.0/60)
	if !near(w.Mean, mean+alpha*4*sd) || w.Var != variance {
		t.Errorf("spike: mean %v (want %v), var %v (want %v)", w.Mean, mean+alpha*4*sd, w.Var, variance)
	}
	if z := w.Z(w.Mean+3*sd, 0); math.Abs(z-3) > 1e-6 {
		t.Errorf("Z = %v", z)
	}
}

func TestDebouncer_ThreeOfFive(t *testing.T) {
	var d anomaly.Debouncer
	got := ""
	for _, raw := range []bool{true, true, false, false, false, true, false, true, false, true, true, false, false, false} {
		if d.Push(raw) {
			got += "1"
		} else {
			got += "0"
		}
	}
	if got != "00000000011100" {
		t.Errorf("debounced = %s", got)
	}
}

func TestHourOfWeek(t *testing.T) {
	berlin := time.FixedZone("CET", 3600)
	for at, want := range map[time.Time]int{
		t0: 0, t0.Add(25*time.Hour + 59*time.Minute): 25, t0.Add(-time.Minute): 167,
		time.Date(2026, 11, 23, 0, 30, 0, 0, berlin): 167, // Sunday 23:30 UTC
	} {
		if got := anomaly.HourOfWeek(at); got != want {
			t.Errorf("HourOfWeek(%v) = %d, want %d", at, got, want)
		}
	}
}

// Without a seasonal baseline (learning) only the short-term one scores: nothing is anomalous;
// NaN is no value.
func TestRobust_NeedsBothBaselines(t *testing.T) {
	var s anomaly.State
	r := anomaly.Robust{}
	for i := range 60 {
		r.Score(&s, t0.Add(time.Duration(i)*time.Minute), float64(i%3), anomaly.Baseline{}, false)
	}
	for i := range 5 {
		v := r.Score(&s, t0.Add(time.Duration(60+i)*time.Minute), 1000, anomaly.Baseline{}, false)
		if v.Short <= anomaly.Threshold || v.Raw || v.Anomalous || !math.IsNaN(v.Expected) || v.Score != 0 {
			t.Fatalf("learning: %+v", v)
		}
	}
	if v := r.Score(&s, t0.Add(2*time.Hour), math.NaN(), anomaly.Baseline{}, false); v.Notable() || v.Short != 0 {
		t.Errorf("NaN = %+v", v)
	}
	// With a seasonal baseline the EWMA starts anew on the deviation from it.
	b := anomaly.Baseline{Median: 1, MAD: 1, Low: 0, High: 2, N: 60}
	v := r.Score(&s, t0.Add(3*time.Hour), 1000, b, true)
	if v.Short != 0 || !s.Residual || s.EWMA.N != 1 || v.Expected != 1 || v.Seasonal < 100 {
		t.Errorf("first with a season: %+v, %+v", v, s.EWMA)
	}
}

// M4-31: a score has an upper bound – a host's extreme values make no score of 1e300 or +Inf (a
// real column holds at most 3.4e38) – and stays anomalous.
func TestRobust_ScoreHasAnUpperBound(t *testing.T) {
	var s anomaly.State
	r := anomaly.Robust{}
	b := anomaly.Baseline{Median: 1, MAD: 1, Low: 0, High: 2, N: 60}
	for i := range 60 {
		r.Score(&s, t0.Add(time.Duration(i)*time.Minute), float64(i%3), b, true)
	}
	for i, x := range []float64{1e300, math.MaxFloat64, 1e40} {
		v := r.Score(&s, t0.Add(time.Duration(60+i)*time.Minute), x, b, true)
		if v.Score != anomaly.MaxScore || !v.Raw {
			t.Errorf("%g: %+v", x, v)
		}
	}
}

// replayed scores the week after four of learning.
func replayed(t *testing.T, seed uint64, spec anomaly.Spec) (anomaly.Synthetic, []anomaly.Verdict, time.Time) {
	t.Helper()
	spec.Start, spec.Weeks = anomaly.BenchmarkStart, 5
	syn := anomaly.Generate(seed, spec)
	return syn, anomaly.Replay(anomaly.Robust{}, syn.Points), spec.Start.Add(4 * 7 * 24 * time.Hour)
}

func flagged(syn anomaly.Synthetic, vs []anomaly.Verdict, from, to time.Time) int {
	n := 0
	for i, v := range vs {
		if t := syn.Points[i].T; v.Anomalous && !t.Before(from) && t.Before(to) {
			n++
		}
	}
	return n
}

// Golden tests per pattern (M4-22).
func TestReplay_GoldenPatterns(t *testing.T) {
	week := 4 * 7 * 24 * time.Hour
	at := func(d time.Duration) time.Time { return anomaly.BenchmarkStart.Add(week + d) }
	h, m := time.Hour, time.Minute
	end := at(7 * 24 * h)
	t.Run("noise and a day cycle are no anomaly", func(t *testing.T) {
		syn, vs, from := replayed(t, 21, anomaly.Spec{Level: 40, Noise: 2, Daily: 20, Weekend: 10})
		if n := flagged(syn, vs, from, end); n != 0 {
			t.Errorf("%d anomalous minutes", n)
		}
	})
	t.Run("a spike is found within the debounce and ends with it", func(t *testing.T) {
		syn, vs, _ := replayed(t, 22, anomaly.Spec{Level: 30, Noise: 1.5, Spikes: []anomaly.Change{{At: week + 10*h, Duration: 15 * m, Size: 10}}})
		if n := flagged(syn, vs, at(10*h), at(10*h+3*m)); n != 1 {
			t.Errorf("first minutes: %d flagged, want the third", n)
		}
		if n := flagged(syn, vs, at(10*h+3*m), at(10*h+15*m)); n != 12 {
			t.Errorf("during: %d of 12", n)
		}
		if n := flagged(syn, vs, at(10*h+18*m), end); n != 0 {
			t.Errorf("after: %d", n)
		}
	})
	t.Run("two minutes are debounced away", func(t *testing.T) {
		syn, vs, from := replayed(t, 23, anomaly.Spec{Level: 30, Noise: 1.5, Spikes: []anomaly.Change{{At: week + 10*h, Duration: 2 * m, Size: 20}}})
		if n := flagged(syn, vs, from, end); n != 0 {
			t.Errorf("%d flagged", n)
		}
	})
	t.Run("the usual peak is none, the same value at night is", func(t *testing.T) {
		syn, vs, _ := replayed(t, 24, anomaly.Spec{Level: 40, Noise: 2, Daily: 20, Spikes: []anomaly.Change{{At: week + 3*h, Duration: 20 * m, Size: 20}}})
		if n := flagged(syn, vs, at(3*h), at(3*h+20*m)); n < 15 {
			t.Errorf("at 03:00 only %d flagged", n)
		}
		if n := flagged(syn, vs, at(13*h), at(15*h)); n != 0 {
			t.Errorf("the usual peak: %d", n)
		}
	})
	t.Run("a nightly job is usual", func(t *testing.T) {
		syn, vs, from := replayed(t, 25, anomaly.Spec{Level: 25, Noise: 2, Routines: []anomaly.Change{{At: 23*h + 45*m, Duration: 10 * m, Size: 8}}})
		if n := flagged(syn, vs, from, end); n != 0 {
			t.Errorf("%d flagged", n)
		}
	})
	t.Run("a level shift is flagged at first, then learnt", func(t *testing.T) {
		syn, vs, _ := replayed(t, 26, anomaly.Spec{Level: 100, Noise: 3, Shifts: []anomaly.Change{{At: week + 24*h, Size: 10}}})
		if n := flagged(syn, vs, at(24*h), at(25*h)); n < 50 {
			t.Errorf("first hour: %d", n)
		}
		if n := flagged(syn, vs, at(32*h), end); n != 0 {
			t.Errorf("after 8 h: %d", n)
		}
	})
	t.Run("a constant series with a small step", func(t *testing.T) {
		syn, vs, from := replayed(t, 27, anomaly.Spec{Level: 8, Noise: 0.01, Shifts: []anomaly.Change{{At: week + 24*h, Size: 3}}})
		if n := flagged(syn, vs, from, end); n != 0 {
			t.Errorf("%d flagged", n)
		}
	})
	t.Run("after a gap the short-term baseline learns again", func(t *testing.T) {
		syn, vs, _ := replayed(t, 28, anomaly.Spec{
			Level: 20, Noise: 1, Gaps: []anomaly.Change{{At: week + 6*h, Duration: 7 * h}},
			Spikes: []anomaly.Change{{At: week + 13*h, Duration: 10 * m, Size: 15}},
		})
		// The spike right after the gap: the EWMA is cold, nothing is flagged.
		if n := flagged(syn, vs, at(13*h), at(14*h)); n != 0 {
			t.Errorf("cold after the gap: %d", n)
		}
	})
}

func TestNotable(t *testing.T) {
	for v, want := range map[anomaly.Verdict]bool{
		{Score: 2.9}: false, {Score: 3}: true, {Anomalous: true}: true,
	} {
		if v.Notable() != want {
			t.Errorf("%+v", v)
		}
	}
}

// A seasonal bucket holds the values of its hour of the last four weeks, none of the hour that
// runs now.
func TestSeasons_FourWeeksOfTheHourBefore(t *testing.T) {
	var s anomaly.Seasons
	week := 7 * 24 * time.Hour
	now := t0.Add(10*time.Hour + 30*time.Minute)
	add := func(at time.Time, v float64, n int) {
		for i := range n {
			s.Add(anomaly.Point{T: at.Add(time.Duration(i) * time.Minute), V: v})
		}
	}
	add(t0.Add(10*time.Hour-5*week), 1000, 60) // five weeks ago: dropped
	for w := 1; w <= 4; w++ {
		add(t0.Add(10*time.Hour-time.Duration(w)*week), float64(w), 15)
	}
	add(t0.Add(10*time.Hour), 500, 20) // this hour: not yet
	b, ok := s.Baseline(now)
	if !ok || b.N != 60 || b.High > 4 || b.Median != 2.5 {
		t.Errorf("baseline = %+v, %v", b, ok)
	}
	if _, ok := s.Baseline(t0.Add(11 * time.Hour)); ok {
		t.Error("another hour has a baseline")
	}
}

// Right after a gap the short-term baseline doesn't score against the old level.
func TestRobust_NoShortTermScoreAfterAGap(t *testing.T) {
	var s anomaly.State
	r := anomaly.Robust{}
	b := anomaly.Baseline{Median: 10, MAD: 1, Low: 8, High: 12, N: 60}
	for i := range 60 {
		r.Score(&s, t0.Add(time.Duration(i)*time.Minute), 10+float64(i%3-1), b, true)
	}
	v := r.Score(&s, t0.Add(59*time.Minute+anomaly.Gap+time.Minute), 30, b, true)
	if v.Short != 0 || v.Seasonal < 10 || v.Raw {
		t.Errorf("after the gap = %+v", v)
	}
}
