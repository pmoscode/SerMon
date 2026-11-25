package anomaly_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/anomaly"
)

var updateBenchmark = flag.Bool("update-benchmark", false, "write the benchmark's reference (testdata/benchmark.json)")

type quality struct {
	Recall, Precision float64
}

type reference struct {
	Total     quality
	Scenarios map[string]quality
}

const referenceFile = "testdata/benchmark.json"

// M4-22: precision and recall of the detector on the synthetic scenarios may not fall below the
// checked-in reference – neither in total nor for a scenario. `just bench-anomaly` shows them;
// after an improvement, `go test -run TestBenchmark ./internal/domain/anomaly/ -update-benchmark`
// raises the reference.
func TestBenchmark_NotWorseThanTheReference(t *testing.T) {
	started := time.Now()
	per, total := anomaly.Benchmark(func(anomaly.Spec) anomaly.Detector { return anomaly.Robust{} })
	got := reference{Total: quality{total.Recall(), total.Precision()}, Scenarios: map[string]quality{}}
	names := make([]string, 0, len(per))
	for name, s := range per {
		got.Scenarios[name] = quality{s.Recall(), s.Precision()}
		names = append(names, name)
	}
	slices.Sort(names)
	var report strings.Builder
	for _, name := range names {
		s := per[name]
		fmt.Fprintf(&report, "%-20s recall %.3f (%d/%d)  precision %.3f (%d flagged, %d false)\n",
			name, s.Recall(), s.Found, s.Events, s.Precision(), s.Flagged, s.FalsePositive)
	}
	fmt.Fprintf(&report, "%-20s recall %.3f  precision %.3f  (%v)", "total", got.Total.Recall, got.Total.Precision, time.Since(started).Round(time.Millisecond))
	t.Log("\n" + report.String())
	if *updateBenchmark {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.MkdirAll(filepath.Dir(referenceFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(referenceFile, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(referenceFile)
	if err != nil {
		t.Fatal(err)
	}
	var want reference
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	worse := func(name string, g, w quality) {
		const eps = 1e-9
		if g.Recall < w.Recall-eps || g.Precision < w.Precision-eps {
			t.Errorf("%s: recall %.3f, precision %.3f – worse than the reference %.3f, %.3f", name, g.Recall, g.Precision, w.Recall, w.Precision)
		}
	}
	worse("total", got.Total, want.Total)
	for name, w := range want.Scenarios {
		g, ok := got.Scenarios[name]
		if !ok {
			t.Errorf("scenario %s is gone", name)
			continue
		}
		worse(name, g, w)
	}
}

// The generator is reproducible and puts the changes where the spec says.
func TestGenerate_IsReproducible(t *testing.T) {
	spec := anomaly.Spec{
		Start: anomaly.BenchmarkStart, Weeks: 1, Level: 10, Noise: 1,
		Spikes: []anomaly.Change{{At: time.Hour, Duration: 10 * time.Minute, Size: 50}},
		Gaps:   []anomaly.Change{{At: 2 * time.Hour, Duration: 30 * time.Minute}},
	}
	a, b := anomaly.Generate(7, spec), anomaly.Generate(7, spec)
	if !slices.Equal(a.Points, b.Points) || len(a.Points) != 7*24*60-30 {
		t.Fatalf("%d points, reproducible %v", len(a.Points), slices.Equal(a.Points, b.Points))
	}
	if c := anomaly.Generate(8, spec); slices.Equal(a.Points, c.Points) {
		t.Error("another seed gives the same series")
	}
	if p := a.Points[65]; p.V < 40 {
		t.Errorf("no spike at 01:05: %+v", p)
	}
	if len(a.Events) != 2 || !a.Events[0].Required || a.Events[1].Required {
		t.Errorf("events = %+v", a.Events)
	}
	counts := anomaly.Generate(1, anomaly.Spec{Start: anomaly.BenchmarkStart, Weeks: 1, Level: 0.2, Noise: 1, Counts: true})
	for _, p := range counts.Points {
		if p.V < 0 || p.V != float64(int(p.V)) {
			t.Fatalf("count %v", p.V)
		}
	}
}

// Evaluate counts found events, flagged minutes and those outside every event.
func TestEvaluate(t *testing.T) {
	start := anomaly.BenchmarkStart
	minute := func(i int) time.Time { return start.Add(time.Duration(i) * time.Minute) }
	syn := anomaly.Synthetic{Events: []anomaly.Event{
		{Start: minute(10), Due: minute(15), Allowed: minute(20), Required: true},
		{Start: minute(30), Due: minute(35), Allowed: minute(40), Required: true},
		{Start: minute(50), Due: minute(50), Allowed: minute(60)},
		{Start: minute(1), Due: minute(5), Allowed: minute(5), Required: true}, // before from
	}}
	var vs []anomaly.Verdict
	for i := range 70 {
		syn.Points = append(syn.Points, anomaly.Point{T: minute(i)})
		anomalous := i == 3 || i == 12 || i == 18 || i == 25 || i == 36 || i == 55
		vs = append(vs, anomaly.Verdict{Anomalous: anomalous})
	}
	s := anomaly.Evaluate(syn, vs, minute(8))
	if s != (anomaly.Score{Events: 2, Found: 1, Flagged: 5, FalsePositive: 1}) || s.Recall() != 0.5 || s.Precision() != 0.8 {
		t.Errorf("score = %+v", s)
	}
	if (anomaly.Score{}).Recall() != 1 || (anomaly.Score{}).Precision() != 1 {
		t.Error("an empty score")
	}
}

// The computing budget of ADR 0037: one minute of 6 000 series, each against its baselines.
func BenchmarkRobust_AMinuteOf6000Series(b *testing.B) {
	states := make([]anomaly.State, 6000)
	baseline := anomaly.Baseline{Median: 50, MAD: 2, Low: 46, High: 54, N: 240}
	r := anomaly.Robust{}
	at := anomaly.BenchmarkStart
	b.ReportAllocs()
	for b.Loop() {
		at = at.Add(time.Minute)
		for i := range states {
			r.Score(&states[i], at, 50+float64(i%7), baseline, true)
		}
	}
}

// A seasonal baseline from four weeks of one hour (240 values), 6 000 of them an hour.
func BenchmarkNewBaseline_240Values(b *testing.B) {
	syn := anomaly.Generate(1, anomaly.Spec{Start: anomaly.BenchmarkStart, Weeks: 1, Level: 50, Noise: 2})
	in := make([]float64, 240)
	for i := range in {
		in[i] = syn.Points[i].V
	}
	b.ReportAllocs()
	for b.Loop() {
		anomaly.NewBaseline(in)
	}
}
