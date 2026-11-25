package anomaly

import "time"

// Score of a detector on synthetic series: Recall is the share of required events it found,
// Precision the share of its anomalous minutes inside an event.
type Score struct {
	Events, Found          int
	Flagged, FalsePositive int
}

// Recall is Found/Events (1 without events).
func (s Score) Recall() float64 {
	if s.Events == 0 {
		return 1
	}
	return float64(s.Found) / float64(s.Events)
}

// Precision is the share of flagged minutes that lie in an event (1 without flags).
func (s Score) Precision() float64 {
	if s.Flagged == 0 {
		return 1
	}
	return float64(s.Flagged-s.FalsePositive) / float64(s.Flagged)
}

// Add sums two scores.
func (s Score) Add(o Score) Score {
	return Score{s.Events + o.Events, s.Found + o.Found, s.Flagged + o.Flagged, s.FalsePositive + o.FalsePositive}
}

// Evaluate scores verdicts (from Replay of syn.Points) from from on.
func Evaluate(syn Synthetic, verdicts []Verdict, from time.Time) Score {
	var s Score
	found := make([]bool, len(syn.Events))
	for i, v := range verdicts {
		t := syn.Points[i].T
		if t.Before(from) || !v.Anomalous {
			continue
		}
		s.Flagged++
		inside := false
		for j, e := range syn.Events {
			if !t.Before(e.Start) && t.Before(e.Allowed) {
				inside = true
			}
			if !t.Before(e.Start) && t.Before(e.Due) {
				found[j] = true
			}
		}
		if !inside {
			s.FalsePositive++
		}
	}
	for j, e := range syn.Events {
		if e.Required && !e.Start.Before(from) {
			s.Events++
			if found[j] {
				s.Found++
			}
		}
	}
	return s
}

// Scenario is a named synthetic series of the benchmark.
type Scenario struct {
	Name string
	Seed uint64
	Spec Spec
}

// BenchmarkStart is a Monday, 00:00 UTC.
var BenchmarkStart = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

// lastWeek is where the benchmark evaluates: after four weeks of learning.
const lastWeek = 4 * 7 * 24 * time.Hour

// day is the start of a day of the evaluated week (0 = Monday) plus an offset.
func day(n int, offset time.Duration) time.Duration {
	return lastWeek + time.Duration(n)*24*time.Hour + offset
}

// Scenarios are the series of the benchmark (M4-22): five weeks each, scored in the last.
func Scenarios() []Scenario {
	h := time.Hour
	m := time.Minute
	base := Spec{Start: BenchmarkStart, Weeks: 5}
	with := func(f func(*Spec)) Spec {
		s := base
		f(&s)
		return s
	}
	return []Scenario{
		{"noise", 1, with(func(s *Spec) { s.Level, s.Noise = 50, 2 })},
		{"seasonal", 2, with(func(s *Spec) { s.Level, s.Noise, s.Daily, s.Weekend = 40, 2, 20, 15 })},
		{"spikes", 3, with(func(s *Spec) {
			s.Level, s.Noise = 30, 1.5
			s.Spikes = []Change{{day(0, 10*h), 10 * m, 12}, {day(2, 3*h), 4 * m, 8}, {day(4, 15*h), 30 * m, 6}, {day(5, 9*h), 2 * m, 15}}
		})},
		{"spikes in a season", 4, with(func(s *Spec) {
			s.Level, s.Noise, s.Daily, s.Weekend = 40, 2, 20, 10
			// At 03:00 the day cycle is low: a value usual at 14:00 is unusual then.
			s.Spikes = []Change{{day(1, 3*h), 20 * m, 20}, {day(3, 14*h), 15 * m, 10}, {day(6, 22*h), 10 * m, 8}}
		})},
		{"level shift", 5, with(func(s *Spec) {
			s.Level, s.Noise = 100, 3
			s.Shifts = []Change{{At: day(2, 12*h), Size: 10}}
		})},
		{"drift", 6, with(func(s *Spec) {
			s.Level, s.Noise = 60, 2
			s.Drifts = []Change{{day(1, 0), 48 * h, 8}}
		})},
		{"gaps", 7, with(func(s *Spec) {
			s.Level, s.Noise, s.Daily = 20, 1, 5
			s.Gaps = []Change{{day(1, 6*h), 3 * h, 0}, {day(4, 0), 30 * m, 0}}
			s.Spikes = []Change{{day(5, 11*h), 10 * m, 10}}
		})},
		{"counts", 8, with(func(s *Spec) {
			s.Level, s.Noise, s.Counts = 3, 1, true
			s.Spikes = []Change{{day(3, 16*h), 10 * m, 12}}
		})},
		{"constant", 9, with(func(s *Spec) {
			// Practically constant; a step of 0.4 % is under the floor of 1 % and no anomaly.
			s.Level, s.Noise = 8, 0.01
			s.Drifts = []Change{{day(2, 0), m, 3}}
		})},
		{"nightly job", 11, with(func(s *Spec) {
			s.Level, s.Noise, s.Daily = 25, 2, 8
			s.Routines = []Change{{2 * h, 30 * m, 15}, {23*h + 45*m, 10 * m, 6}}
		})},
		{"outliers", 12, with(func(s *Spec) {
			s.Level, s.Noise, s.Outliers = 70, 3, 0.02
			s.Spikes = []Change{{day(2, 8*h), 20 * m, 9}}
		})},
		{"weak spikes", 13, with(func(s *Spec) {
			s.Level, s.Noise, s.Daily = 200, 10, 30
			s.Spikes = []Change{{day(0, 9*h), 15 * m, 5}, {day(1, 13*h), 20 * m, 4.5}, {day(3, 4*h), 10 * m, 5.5}, {day(5, 18*h), 30 * m, 4}}
		})},
		{"noisy spikes", 10, with(func(s *Spec) {
			s.Level, s.Noise, s.Daily = 500, 40, 100
			s.Spikes = []Change{{day(0, 20*h), 15 * m, 9}, {day(3, 5*h), 6 * m, 7}, {day(6, 12*h), 20 * m, 10}}
		})},
	}
}

// Benchmark runs d on all scenarios and returns the score per scenario and the total.
func Benchmark(d func(Spec) Detector) (map[string]Score, Score) {
	per := map[string]Score{}
	var total Score
	for _, sc := range Scenarios() {
		syn := Generate(sc.Seed, sc.Spec)
		s := Evaluate(syn, Replay(d(sc.Spec), syn.Points), sc.Spec.Start.Add(lastWeek))
		per[sc.Name] = s
		total = total.Add(s)
	}
	return per, total
}
