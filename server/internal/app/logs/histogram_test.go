package logs_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// fakeCounts records the queries of the counts counted at the ingest (V1-24).
type fakeCounts struct {
	orgs    []tenant.OrgID
	queries []logs.CountQuery
	err     error
}

func (f *fakeCounts) Counts(_ context.Context, o tenant.OrgID, q logs.CountQuery) ([]logs.CountSeries, error) {
	f.orgs = append(f.orgs, o)
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return []logs.CountSeries{{Labels: map[string]string{"severity": "warning"}, Points: []logs.CountPoint{{At: q.Until, Value: 7}}}}, nil
}

func counted(store *fakeStore, counts *fakeCounts) *logs.Service {
	return logs.NewService(store, fakeDirectory{}, authz.NewPolicy(), logs.WithCounts(counts))
}

// About HistogramSteps buckets; from a minute on in whole minutes and from half an hour on in
// whole hours, the buckets of the counts.
func TestHistogramStep_FitsTheBucketsOfTheCounts(t *testing.T) {
	for span, want := range map[time.Duration]time.Duration{
		10 * time.Second:    time.Second,
		time.Hour:           36 * time.Second,
		99 * time.Minute:    59 * time.Second,
		100 * time.Minute:   time.Minute,
		2 * time.Hour:       2 * time.Minute,
		24 * time.Hour:      15 * time.Minute,
		49 * time.Hour:      30 * time.Minute,
		50 * time.Hour:      time.Hour,
		51 * time.Hour:      time.Hour,
		7 * 24 * time.Hour:  2 * time.Hour,
		14 * 24 * time.Hour: 4 * time.Hour,
	} {
		if got := logs.HistogramStep(span); got != want {
			t.Errorf("HistogramStep(%v) = %v, want %v", span, got, want)
		}
	}
}

// V1-24: without text or units the histogram of a range of 100 minutes and more comes from the
// counts, for the visible nodes only, at points aligned to the step; Loki is not asked.
func TestHistogram_LongRangesWithoutTextComeFromTheCounts(t *testing.T) {
	store, counts := &fakeStore{}, &fakeCounts{}
	f := logs.Filter{From: now.Add(-14*24*time.Hour + 17*time.Minute), To: now.Add(-3 * time.Minute)}
	f.Sources = []logql.Source{logql.SourceJournal}
	f.MinSeverity = logql.SeverityWarning
	h, err := counted(store, counts).Histogram(context.Background(), viewerOf(web), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 0 || len(counts.queries) != 1 || counts.orgs[0] != org {
		t.Fatalf("Loki %d calls, counts %+v", len(store.calls), counts.queries)
	}
	q := counts.queries[0]
	step := 4 * time.Hour
	if q.Step != step || h.Step != step {
		t.Errorf("step %v / %v, want %v", q.Step, h.Step, step)
	}
	// now is 12:00 UTC: the points are at 0, 4, 8, … h UTC, the first after the start.
	wantAfter := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	if !q.After.Equal(wantAfter) || !q.Until.Equal(now) {
		t.Errorf("points after %v until %v, want after %v until %v", q.After, q.Until, wantAfter, now)
	}
	slices.SortFunc(q.Nodes, func(a, b identity.NodeID) int { return strings.Compare(a.String(), b.String()) })
	if !slices.Equal(q.Nodes, []identity.NodeID{web01, web02}) {
		t.Errorf("nodes %v, want those of the group web", q.Nodes)
	}
	if !slices.Equal(q.Sources, f.Sources) || !slices.Equal(q.Severities, logql.Severities[:5]) {
		t.Errorf("sources %v, severities %v", q.Sources, q.Severities)
	}
	if p := h.Series[logql.SeverityWarning]; len(p) != 1 || p[0].Value != 7 {
		t.Errorf("series %+v", h.Series)
	}

	// The minute buckets: a day in 15 minutes, aligned the same way.
	f = logs.Filter{From: now.Add(-24*time.Hour - 7*time.Minute), To: now.Add(-time.Second)}
	if _, err := counted(store, counts).Histogram(context.Background(), admin(), f); err != nil {
		t.Fatal(err)
	}
	q = counts.queries[1]
	if q.Step != 15*time.Minute || !q.After.Equal(now.Add(-24*time.Hour-15*time.Minute)) || !q.Until.Equal(now) || len(q.Nodes) != 3 {
		t.Errorf("a day: %+v", q)
	}
}

// Text and units are not counted: Loki answers, with the same aligned points; so do ranges
// shorter than the counts' minute.
func TestHistogram_TextUnitsAndShortRangesAskLokiAtTheSamePoints(t *testing.T) {
	long := logs.Filter{From: now.Add(-24*time.Hour - 7*time.Minute), To: now.Add(-time.Second)}
	text, units := long, long
	text.Text = "timed out"
	units.Units = []string{"nginx.service"}
	short := logs.Filter{From: now.Add(-time.Hour - 10*time.Second), To: now.Add(-10 * time.Second)}
	for name, tc := range map[string]struct {
		f           logs.Filter
		step        time.Duration
		first, last time.Time
	}{
		"text":  {text, 15 * time.Minute, now.Add(-24*time.Hour - 15*time.Minute), now},
		"units": {units, 15 * time.Minute, now.Add(-24*time.Hour - 15*time.Minute), now},
		"short": {short, 36 * time.Second, now.Add(-time.Hour - 36*time.Second), now},
	} {
		store, counts := &fakeStore{}, &fakeCounts{}
		h, err := counted(store, counts).Histogram(context.Background(), admin(), tc.f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(counts.queries) != 0 || len(store.calls) != 1 {
			t.Fatalf("%s: counts %d, Loki %d", name, len(counts.queries), len(store.calls))
		}
		// Loki's points: from its start to its end in steps, each counting (t − step, t]; it
		// starts a point early, which is dropped.
		c := store.calls[0]
		if c.step != tc.step || h.Step != tc.step || !c.from.Equal(tc.first) || !c.to.Equal(tc.last) {
			t.Errorf("%s: Loki from %v to %v in %v, want %v to %v in %v", name, c.from, c.to, c.step, tc.first, tc.last, tc.step)
		}
	}
}

// Loki is asked from a point before the range (it sometimes leaves out lines of its first point);
// that point is dropped, the others stay as Loki counted them.
func TestHistogram_LokisPointBeforeTheRangeIsDropped(t *testing.T) {
	f := logs.Filter{From: now.Add(-time.Hour), To: now}
	store := &fakeStore{}
	h, err := service(store).Histogram(context.Background(), admin(), f)
	if err != nil {
		t.Fatal(err)
	}
	// fakeStore answers a point at its start: before the range.
	if c := store.calls[0]; !c.from.Equal(now.Add(-time.Hour)) || len(h.Series[logql.SeverityError]) != 0 {
		t.Errorf("from %v, series %+v", c.from, h.Series)
	}
	store.points = []time.Time{now.Add(-time.Hour), now.Add(-time.Hour + 36*time.Second), now}
	h, _ = service(store).Histogram(context.Background(), admin(), f)
	if p := h.Series[logql.SeverityError]; len(p) != 2 || !p[0].At.Equal(now.Add(-time.Hour+36*time.Second)) {
		t.Errorf("points %+v, want the two after the start", p)
	}
}

// Without counts (no database) Loki answers as before; a failing count store is an error, not
// a query of Loki over the whole range.
func TestHistogram_WithoutCountsOrWhenTheyFail(t *testing.T) {
	f := logs.Filter{From: now.Add(-7 * 24 * time.Hour), To: now}
	store := &fakeStore{}
	if _, err := service(store).Histogram(context.Background(), admin(), f); err != nil || len(store.calls) != 1 || store.calls[0].step != 2*time.Hour {
		t.Errorf("without counts: %v, %+v", err, store.calls)
	}
	broken := &fakeCounts{err: errors.New("database down")}
	store = &fakeStore{}
	if _, err := counted(store, broken).Histogram(context.Background(), admin(), f); err == nil || len(store.calls) != 0 {
		t.Errorf("failing counts: %v, Loki calls %d", err, len(store.calls))
	}
}

// Loki too slow for a text over a long range: the caller learns to narrow it (FAILED_PRECONDITION).
func TestHistogram_ATimeoutOfLokiIsTooBroad(t *testing.T) {
	store := &fakeStore{countErr: logs.ErrTooBroad}
	f := logs.Filter{From: now.Add(-7 * 24 * time.Hour), To: now, Text: "x"}
	if _, err := counted(store, &fakeCounts{}).Histogram(context.Background(), admin(), f); !errors.Is(err, logs.ErrTooBroad) {
		t.Errorf("err = %v, want ErrTooBroad", err)
	}
}
