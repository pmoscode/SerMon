package anomalies_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/anomaly"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	orgB  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1"))
	node1 = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	node2 = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	// A Wednesday, 10:30 UTC; the nodes are older than the learning phase.
	now0 = time.Date(2026, 11, 25, 10, 30, 0, 0, time.UTC)
	old  = now0.Add(-60 * 24 * time.Hour)
)

// fake is the database: series with values from a function, baselines, results, leases.
type fake struct {
	mu        sync.Mutex
	series    map[tenant.OrgID][]anomalies.Series
	value     func(id int64, t time.Time) (float64, bool)
	baselines map[tenant.OrgID]map[int]map[int64]anomaly.Baseline
	ages      map[tenant.OrgID]map[int]time.Time
	notable   map[tenant.OrgID][]anomalies.Notable
	rates     map[tenant.OrgID][]metrics.Aggregate
	holders   map[tenant.OrgID]string
	reads     int
	asked     map[int64]bool // the series whose points were read
	saved     map[int]int    // how often each hour of the week was computed
	fail      error
	// onPoints runs at each read of an organization's points (a slow database for one).
	onPoints func(tenant.OrgID)
	readsOf  map[tenant.OrgID]int
	// released are the organizations given up; checks counts the lease checks of the writes,
	// onSave runs before each (the caller holds f.mu).
	released []tenant.OrgID
	checks   int
	onSave   func(tenant.OrgID)
}

func newFake() *fake {
	return &fake{
		series:    map[tenant.OrgID][]anomalies.Series{},
		value:     func(id int64, t time.Time) (float64, bool) { return 50 + float64((t.Minute()+int(id))%5), true },
		baselines: map[tenant.OrgID]map[int]map[int64]anomaly.Baseline{},
		ages:      map[tenant.OrgID]map[int]time.Time{},
		notable:   map[tenant.OrgID][]anomalies.Notable{},
		rates:     map[tenant.OrgID][]metrics.Aggregate{},
		holders:   map[tenant.OrgID]string{},
	}
}

func (f *fake) Organizations(context.Context) ([]tenant.OrgID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tenant.OrgID
	for org := range f.series {
		out = append(out, org)
	}
	slices.SortFunc(out, func(a, b tenant.OrgID) int { return slices.Compare(a[:], b[:]) })
	return out, f.fail
}

func (f *fake) Lease(_ context.Context, org tenant.OrgID, holder string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h, ok := f.holders[org]; ok && h != holder {
		return false, nil
	}
	f.holders[org] = holder
	return true, nil
}

func (f *fake) Release(_ context.Context, org tenant.OrgID, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holders[org] == holder {
		delete(f.holders, org)
		f.released = append(f.released, org)
	}
	return nil
}

// held is the lease check of the writes (the caller holds f.mu).
func (f *fake) held(org tenant.OrgID, holder string) error {
	if f.holders[org] != holder {
		return anomalies.ErrLeaseLost
	}
	return nil
}

func (f *fake) Series(_ context.Context, org tenant.OrgID, names []string, _ time.Time, perNode, limit int) ([]anomalies.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []anomalies.Series
	of := map[identity.NodeID]int{}
	for _, s := range f.series[org] {
		if slices.Contains(names, s.Metric) && len(out) < limit && of[s.Node] < perNode {
			of[s.Node]++
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fake) Points(_ context.Context, org tenant.OrgID, ids []int64, from, to time.Time) ([]anomalies.Point, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.readsOf == nil {
		f.readsOf = map[tenant.OrgID]int{}
	}
	f.readsOf[org]++
	if f.onPoints != nil {
		f.onPoints(org)
	}
	if f.asked == nil {
		f.asked = map[int64]bool{}
	}
	var out []anomalies.Point
	for _, id := range ids {
		f.asked[id] = true
		for t := from; t.Before(to); t = t.Add(time.Minute) {
			if v, ok := f.value(id, t); ok {
				// In UTC, as the store returns them.
				out = append(out, anomalies.Point{Series: id, T: t.UTC(), V: v})
			}
		}
	}
	return out, nil
}

func (f *fake) Baselines(_ context.Context, org tenant.OrgID, hour int) (map[int64]anomaly.Baseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.baselines[org][hour], nil
}

func (f *fake) SaveBaselines(_ context.Context, org tenant.OrgID, holder string, hour int, at time.Time, b map[int64]anomaly.Baseline) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.held(org, holder); err != nil {
		return err
	}
	if f.baselines[org] == nil {
		f.baselines[org], f.ages[org] = map[int]map[int64]anomaly.Baseline{}, map[int]time.Time{}
	}
	if f.saved == nil {
		f.saved = map[int]int{}
	}
	f.saved[hour]++
	// Like PostgreSQL: the baselines replace the hour's (review M4-30), its age comes from its
	// rows; an hour without any is missing.
	if len(b) == 0 {
		delete(f.baselines[org], hour)
		delete(f.ages[org], hour)
		return nil
	}
	f.baselines[org][hour], f.ages[org][hour] = b, at
	return nil
}

func (f *fake) BaselineAges(_ context.Context, org tenant.OrgID) (map[int]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ages[org], nil
}

func (f *fake) SaveNotable(_ context.Context, org tenant.OrgID, holder string, n []anomalies.Notable) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.onSave != nil {
		f.onSave(org)
	}
	if err := f.held(org, holder); err != nil {
		return err
	}
	f.notable[org] = append(f.notable[org], n...)
	return nil
}

func (f *fake) Write(_ context.Context, org tenant.OrgID, a []metrics.Aggregate, _ map[identity.NodeID]uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rates[org] = append(f.rates[org], a...)
	return nil
}

// clock is the worker's time; each reading moves it on by step (a slow database).
type clock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.t
	c.t = c.t.Add(c.step)
	return t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func worker(t *testing.T, f *fake, c *clock, tune func(*anomalies.Config)) *anomalies.Worker {
	t.Helper()
	config := anomalies.DefaultConfig("a", slog.New(slog.DiscardHandler))
	config.Now = c.Now
	config.Backfill = 168
	if tune != nil {
		tune(&config)
	}
	return anomalies.NewWorker(f, f, config)
}

func cpu(id int64, node identity.NodeID, mode string) anomalies.Series {
	return anomalies.Series{ID: id, Node: node, Metric: "sermon_cpu_utilization_ratio", Labels: map[string]string{"mode": mode}, Created: old, Learning: old}
}

func lastRates(f *fake, org tenant.OrgID, node identity.NodeID, metric string) []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []float64
	for _, a := range f.rates[org] {
		if a.Node == node && a.Metric == metric {
			out = append(out, a.Avg)
		}
	}
	return out
}

// M4-23: the first round warms up on 6 h and computes the baselines of the whole week; from the
// next round on a spike of one series is anomalous, stored as notable minutes, and makes up its
// node's rate. Results are written only for the last minutes, never for the warm-up.
func TestWorker_FindsASpikeAndWritesTheRate(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user"), cpu(2, node1, "system"), cpu(3, node1, "idle"), cpu(4, node2, "user")}
	spike := now0.Add(2 * time.Minute)
	base := f.value
	f.value = func(id int64, t time.Time) (float64, bool) {
		v, ok := base(id, t)
		if id == 2 && !t.Before(spike) && t.Before(spike.Add(10*time.Minute)) {
			v += 500_000
		}
		return v, ok
	}
	c := &clock{t: now0}
	w := worker(t, f, c, nil)
	w.Round(context.Background())
	if n := len(f.ages[orgA]); n != 168 {
		t.Fatalf("%d hours of baselines after the first round", n)
	}
	if got := w.Stats().Scored.Load(); got != (6*60-3)*3 {
		t.Errorf("warm-up scored %d", got)
	}
	// Only the last five ready minutes are written: 10:22 to 10:26 (10:27 isn't ready).
	if n := len(lastRates(f, orgA, node1, "sermon_anomaly_rate_ratio")); n != 5 {
		t.Errorf("%d rates written in the first round", n)
	}
	for i := 1; i <= 35; i++ {
		c.set(now0.Add(time.Duration(i) * time.Minute))
		w.Round(context.Background())
	}
	// The hour of 10:00 ended at 11:00 (ready at 11:03): its bucket learnt it, from four weeks.
	if at := f.ages[orgA][anomaly.HourOfWeek(now0)]; !at.Equal(now0.Add(33 * time.Minute)) {
		t.Errorf("the hour of 10:00 was computed at %v, not after it ended", at)
	}
	if b := f.baselines[orgA][anomaly.HourOfWeek(now0)][1]; b.N != 240 {
		t.Errorf("baseline from %d values, want 4 weeks × 60", b.N)
	}
	// Fresh hours are not computed again: 168, the 6 that ended in the warm-up, then 10:00.
	if got := w.Stats().Baselines.Load(); got != 168+6+1 {
		t.Errorf("%d baselines computed", got)
	}
	var found []time.Time
	notAnomalous := 0
	for _, n := range f.notable[orgA] {
		if !n.Anomalous {
			notAnomalous++
		}
		if n.Series != 2 {
			t.Errorf("notable minute of series %d: %+v", n.Series, n)
		}
		if n.Anomalous {
			found = append(found, n.T)
		}
	}
	if len(found) < 6 || found[0].Before(spike) || found[0].After(spike.Add(3*time.Minute)) {
		t.Fatalf("anomalous minutes = %v", found)
	}
	// The first minutes of the spike are notable before the debounce makes them anomalous.
	if notAnomalous == 0 {
		t.Error("no notable minute that isn't anomalous")
	}
	rates := lastRates(f, orgA, node1, "sermon_anomaly_rate_ratio")
	if peak := slices.Max(rates); peak != 500_000 {
		t.Errorf("rate of node 1 peaks at %v, want half of its two scored series", peak)
	}
	// The first minute of the spike is not yet anomalous (debounced), the rate is still 0.
	f.mu.Lock()
	for _, a := range f.rates[orgA] {
		if a.Node == node1 && a.Metric == "sermon_anomaly_rate_ratio" && a.Start.Equal(spike) && a.Avg != 0 {
			t.Errorf("rate at the spike's first minute = %v", a.Avg)
		}
	}
	f.mu.Unlock()
	if scored := lastRates(f, orgA, node1, "sermon_anomaly_series_scored"); scored[len(scored)-1] != 2 {
		t.Errorf("scored = %v (idle is no candidate)", scored)
	}
	if slices.Max(lastRates(f, orgA, node2, "sermon_anomaly_rate_ratio")) != 0 {
		t.Error("node 2 has an anomaly")
	}
	// Every minute once.
	minutes := map[time.Time]int{}
	f.mu.Lock()
	for _, a := range f.rates[orgA] {
		if a.Node == node1 && a.Metric == "sermon_anomaly_rate_ratio" {
			minutes[a.Start]++
		}
	}
	f.mu.Unlock()
	for m, n := range minutes {
		if n != 1 {
			t.Errorf("%v written %d times", m, n)
		}
	}
}

// A learning node is scored but neither stored nor counted: its rate stays 0. A series younger
// than the learning phase on an old node, too.
func TestWorker_NoResultsWhileLearning(t *testing.T) {
	f := newFake()
	young := cpu(1, node1, "user")
	young.Learning = now0.Add(-3 * 24 * time.Hour)
	fresh := cpu(2, node2, "user")
	fresh.Created = now0.Add(-time.Hour)
	f.series[orgA] = []anomalies.Series{young, fresh}
	f.value = func(_ int64, t time.Time) (float64, bool) {
		if t.After(now0.Add(-10 * time.Minute)) {
			return 900_000, true
		}
		return 50 + float64(t.Minute()%5), true
	}
	c := &clock{t: now0}
	w := worker(t, f, c, nil)
	for i := range 10 {
		c.set(now0.Add(time.Duration(i) * time.Minute))
		w.Round(context.Background())
	}
	if len(f.notable[orgA]) != 0 {
		t.Errorf("notable while learning: %+v", f.notable[orgA][0])
	}
	for _, n := range []identity.NodeID{node1, node2} {
		rates := lastRates(f, orgA, n, "sermon_anomaly_rate_ratio")
		if len(rates) == 0 || slices.Max(rates) != 0 {
			t.Errorf("%v: rates %v", n, rates)
		}
		if scored := lastRates(f, orgA, n, "sermon_anomaly_series_scored"); slices.Max(scored) != 0 {
			t.Errorf("%v: scored %v", n, scored)
		}
	}
}

// One instance per organization: the other skips it; a lost lease drops the state, a takeover
// warms up again.
func TestWorker_OneInstancePerOrganization(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	f.series[orgB] = []anomalies.Series{cpu(2, node2, "user")}
	f.holders[orgB] = "b"
	c := &clock{t: now0}
	a := worker(t, f, c, nil)
	a.Round(context.Background())
	if len(f.rates[orgB]) != 0 || len(f.rates[orgA]) == 0 {
		t.Fatalf("a wrote for B: %d, for A: %d", len(f.rates[orgB]), len(f.rates[orgA]))
	}
	f.mu.Lock()
	f.holders[orgA] = "b"
	f.mu.Unlock()
	c.set(now0.Add(time.Minute))
	a.Round(context.Background())
	if a.Stats().LeasesLost.Load() != 1 {
		t.Errorf("leases lost = %d", a.Stats().LeasesLost.Load())
	}
	f.mu.Lock()
	delete(f.holders, orgA)
	before := f.reads
	f.mu.Unlock()
	c.set(now0.Add(2 * time.Minute))
	a.Round(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads-before < 6 {
		t.Errorf("no warm-up after the takeover: %d reads", f.reads-before)
	}
}

// A round stops at its budget and the next one continues where it stopped; nothing piles up.
func TestWorker_ContinuesAfterTheBudget(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	c := &clock{t: now0, step: 3 * time.Second}
	w := worker(t, f, c, func(cfg *anomalies.Config) { cfg.Budget = 10 * time.Second; cfg.Backfill = 0 })
	w.Round(context.Background())
	if w.Stats().Overruns.Load() != 1 {
		t.Fatalf("overruns = %d", w.Stats().Overruns.Load())
	}
	first := w.Stats().Scored.Load()
	for range 10 {
		w.Round(context.Background())
	}
	// 6 h of warm-up plus the minutes the clock moved on, each once.
	elapsed := c.Now().Sub(now0)
	want := uint64((6*time.Hour + elapsed - 3*time.Minute) / time.Minute)
	if got := w.Stats().Scored.Load(); got <= first || got > want {
		t.Errorf("scored %d after the rounds, at most %d", got, want)
	}
}

// At most PerNode series per node (the catalog's order first), PerOrg per organization and
// PerInstance in all.
func TestWorker_Limits(t *testing.T) {
	f := newFake()
	var a []anomalies.Series
	for i := range 5 {
		a = append(a, anomalies.Series{ID: int64(10 + i), Node: node1, Metric: "sermon_disk_busy_ratio", Labels: map[string]string{"device": string(rune('a' + i))}, Created: old, Learning: old})
	}
	a = append(a, cpu(1, node1, "user"), cpu(2, node2, "user"), cpu(3, node2, "steal"))
	f.series[orgA] = a
	f.series[orgB] = []anomalies.Series{cpu(4, node1, "user"), cpu(5, node1, "system")}
	c := &clock{t: now0}
	w := worker(t, f, c, func(cfg *anomalies.Config) { cfg.PerNode, cfg.PerOrg, cfg.PerInstance = 3, 4, 5 })
	w.Round(context.Background())
	scored := func(org tenant.OrgID, node identity.NodeID) float64 {
		s := lastRates(f, org, node, "sermon_anomaly_series_scored")
		if len(s) == 0 {
			return math.NaN()
		}
		return s[len(s)-1]
	}
	// The round begins with org B (the order turns): both of its series; then org A gets the 3
	// left of the instance's 5, all on node 1 (cpu first), node 2 none.
	if scored(orgA, node1) != 3 || !math.IsNaN(scored(orgA, node2)) || scored(orgB, node1) != 2 {
		t.Errorf("scored: A %v/%v, B %v", scored(orgA, node1), scored(orgA, node2), scored(orgB, node1))
	}
	if w.Stats().Skipped.Load() != 5 {
		t.Errorf("skipped = %d", w.Stats().Skipped.Load())
	}
	// The catalog's order: the cpu series of node 1 came before its disks.
	if !f.asked[1] || f.asked[12] {
		t.Errorf("read %v, want cpu 1 and the first two disks", f.asked)
	}
	// With room enough, the limit per node binds alone.
	f2 := newFake()
	f2.series[orgA] = a
	w2 := worker(t, f2, &clock{t: now0}, func(cfg *anomalies.Config) { cfg.PerNode = 2 })
	w2.Round(context.Background())
	got1, got2 := lastRates(f2, orgA, node1, "sermon_anomaly_series_scored"), lastRates(f2, orgA, node2, "sermon_anomaly_series_scored")
	if got1[len(got1)-1] != 2 || got2[len(got2)-1] != 2 || w2.Stats().Skipped.Load() != 4 {
		t.Errorf("per node: %v, %v, skipped %d", got1, got2, w2.Stats().Skipped.Load())
	}
}

// A counter is scored by its increase per minute; a restart of the agent (a smaller value) is
// left out. The baselines see the increases, too.
func TestWorker_CountersByTheirIncrease(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{{ID: 1, Node: node1, Metric: "sermon_agent_logs_sent_entries", Labels: map[string]string{}, Created: old, Learning: old}}
	f.value = func(_ int64, t time.Time) (float64, bool) {
		minutes := float64(t.Sub(old) / time.Minute)
		v := minutes * 20 // 20 lines a minute
		// The agent restarted twice: at 07:30, and in the minutes written (10:24); the counter
		// began anew each time.
		for _, restart := range []time.Time{now0.Add(-3 * time.Hour), now0.Add(-6 * time.Minute)} {
			if !t.Before(restart) {
				v -= float64(restart.Sub(old)/time.Minute) * 20
			}
		}
		if t.After(now0.Add(-30 * time.Minute)) {
			v += float64(t.Sub(now0.Add(-30*time.Minute))/time.Minute) * 2000
		}
		return v, true
	}
	c := &clock{t: now0}
	w := worker(t, f, c, nil)
	w.Round(context.Background())
	b := f.baselines[orgA][anomaly.HourOfWeek(now0.Add(-2*time.Hour))][1]
	if b.Median != 20 {
		t.Errorf("baseline of the increase = %+v", b)
	}
	// Each of the four hours misses its first increase; the hour of the restart one more.
	if got := f.baselines[orgA][anomaly.HourOfWeek(now0.Add(-3*time.Hour))][1].N; got != 4*59-1 {
		t.Errorf("baseline of the restart's hour from %d values", got)
	}
	// The minute of the restart is not scored: no rate for it.
	f.mu.Lock()
	for _, a := range f.rates[orgA] {
		if a.Start.Equal(now0.Add(-6 * time.Minute)) {
			t.Errorf("the restart was scored: %+v", a)
		}
	}
	f.mu.Unlock()
	if !slices.ContainsFunc(f.notable[orgA], func(n anomalies.Notable) bool { return n.Anomalous }) {
		t.Error("100× the log rate is no anomaly")
	}
	// The restart is no anomaly (no huge negative or positive increase), the spike is.
	for _, n := range f.notable[orgA] {
		if n.T.Before(now0.Add(-30 * time.Minute)) {
			t.Errorf("notable before the spike: %+v", n)
		}
	}
}

func TestWorker_ListingFails(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	f.fail = errors.New("database down")
	c := &clock{t: now0}
	w := worker(t, f, c, nil)
	w.Round(context.Background())
	if w.Stats().Errors.Load() != 1 || len(f.rates[orgA]) != 0 {
		t.Errorf("errors = %d", w.Stats().Errors.Load())
	}
}

// After a takeover the warm-up scores against the baselines of its hours at once, even without
// any other backfill: a spike in the first minutes written is found in the very first round.
func TestWorker_TheWarmUpHasItsBaselines(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	spike := now0.Add(-8 * time.Minute) // in the five minutes the first round writes
	base := f.value
	f.value = func(id int64, t time.Time) (float64, bool) {
		v, ok := base(id, t)
		if !t.Before(spike) {
			v += 500_000
		}
		return v, ok
	}
	w := worker(t, f, &clock{t: now0}, func(cfg *anomalies.Config) { cfg.Backfill = 0 })
	w.Round(context.Background())
	// 04:00 to 10:00: the seven hours of the warm-up, each from the weeks before.
	if got := w.Stats().Baselines.Load(); got < 7 {
		t.Fatalf("%d baselines", got)
	}
	for h := now0.Add(-6 * time.Hour).Truncate(time.Hour); !h.After(now0); h = h.Add(time.Hour) {
		if _, ok := f.ages[orgA][anomaly.HourOfWeek(h)]; !ok {
			t.Errorf("no baseline for %v", h)
		}
	}
	if !slices.ContainsFunc(f.notable[orgA], func(n anomalies.Notable) bool { return n.Anomalous }) {
		t.Error("the spike is not found in the first round")
	}
}

// The other missing hours come from the current one forwards, Backfill per round; an hour
// without data is tried again only an hour later, not every round.
func TestWorker_BackfillsTheHoursNeededNextAndRetriesHourly(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	f.value = func(int64, time.Time) (float64, bool) { return 0, false } // no data at all
	c := &clock{t: now0}
	w := worker(t, f, c, func(cfg *anomalies.Config) { cfg.Backfill = 2 })
	w.Round(context.Background())
	eleven := anomaly.HourOfWeek(now0.Add(30 * time.Minute)) // 11:00, the first after the warm-up
	if f.saved[eleven] != 1 || f.saved[anomaly.HourOfWeek(now0.Add(90*time.Minute))] != 1 {
		t.Errorf("after the warm-up's hours not the next ones: %v", f.saved)
	}
	before := len(f.saved)
	c.set(now0.Add(time.Minute))
	w.Round(context.Background())
	if f.saved[eleven] != 1 || len(f.saved) != before+2 {
		t.Errorf("second round: %d hours more, 11:00 computed %d times", len(f.saved)-before, f.saved[eleven])
	}
	c.set(now0.Add(61 * time.Minute))
	w.Round(context.Background())
	if f.saved[eleven] != 2 {
		t.Errorf("an hour later 11:00 computed %d times, want tried again", f.saved[eleven])
	}

	// With data, after the warm-up's hours the ones right after now, Backfill of them.
	g := newFake()
	g.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	w2 := worker(t, g, &clock{t: now0}, func(cfg *anomalies.Config) { cfg.Backfill = 2 })
	w2.Round(context.Background())
	for _, h := range []time.Duration{30 * time.Minute, 90 * time.Minute} {
		if _, ok := g.ages[orgA][anomaly.HourOfWeek(now0.Add(h))]; !ok {
			t.Errorf("the hour at %v after now has no baseline", h)
		}
	}
	if _, ok := g.ages[orgA][anomaly.HourOfWeek(now0.Add(150*time.Minute))]; ok {
		t.Error("more than Backfill hours computed")
	}
	// A fresh hour (with data) is not computed again, also after the retry of missing ones.
	w3 := worker(t, g, &clock{t: now0.Add(61 * time.Minute)}, func(cfg *anomalies.Config) { cfg.Backfill = 2 })
	w3.Round(context.Background())
	if n := g.saved[eleven]; n != 1 {
		t.Errorf("the fresh 11:00 computed %d times", n)
	}
}

// Review M4-30: an organization that runs out of the budget every round doesn't take the others'
// turns: the next round starts after it, every other one is served every round.
func TestWorker_ABigOrganizationLeavesTheOthersTheirTurn(t *testing.T) {
	f := newFake()
	orgs := []tenant.OrgID{orgA}
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	for i := range 7 {
		org := tenant.OrgID(uuid.MustParse(fmt.Sprintf("0193f5a0-0000-7000-8000-0000000000c%d", i)))
		orgs = append(orgs, org)
		f.series[org] = []anomalies.Series{cpu(int64(10+i), identity.NodeID(uuid.New()), "user")}
	}
	c := &clock{t: now0}
	w := worker(t, f, c, func(cfg *anomalies.Config) { cfg.Backfill = 0 })
	// orgA (the first by ID) spends the whole budget at its first read.
	f.onPoints = func(org tenant.OrgID) {
		if org == orgA {
			c.mu.Lock()
			c.t = c.t.Add(time.Hour)
			c.mu.Unlock()
		}
	}
	for round := range 10 {
		before := map[tenant.OrgID]int{}
		for _, org := range orgs {
			before[org] = f.readsOf[org]
		}
		c.set(now0.Add(time.Duration(round) * time.Minute))
		w.Round(context.Background())
		for _, org := range orgs[1:] {
			if f.readsOf[org] == before[org] && round > 0 {
				t.Errorf("round %d: %v got no turn", round, org)
			}
		}
	}
}

// Review M4-30: after the takeover's warm-up every minute is written, also those of a longer wait
// between two turns (more than Output).
func TestWorker_WritesEveryMinuteAfterAWait(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	c := &clock{t: now0}
	w := worker(t, f, c, func(cfg *anomalies.Config) { cfg.Backfill = 0 })
	for _, at := range []time.Duration{0, 12 * time.Minute, 30 * time.Minute} {
		c.set(now0.Add(at))
		w.Round(context.Background())
	}
	minutes := map[time.Time]bool{}
	f.mu.Lock()
	for _, a := range f.rates[orgA] {
		if a.Metric == "sermon_anomaly_rate_ratio" {
			minutes[a.Start] = true
		}
	}
	f.mu.Unlock()
	first := now0.Add(-8 * time.Minute) // the takeover's Output (Lag 3 min, Output 5 min)
	last := now0.Add(27 * time.Minute)
	for m := first; m.Before(last); m = m.Add(time.Minute) {
		if !minutes[m] {
			t.Errorf("no rate for %v", m.Format("15:04"))
		}
	}
	if minutes[first.Add(-time.Minute)] {
		t.Error("a minute of the warm-up was written")
	}
}

// The server's clock runs in time.Local, the store answers in UTC: the same minutes, other
// locations. Found in the demo (M4-28): the worker scored nothing and wrote no rate.
func TestWorker_ScoresWhateverTheLocationOfTheClock(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	c := &clock{t: now0.In(time.FixedZone("CET", 3600))}
	w := worker(t, f, c, nil)
	w.Round(context.Background())
	if got := w.Stats().Scored.Load(); got == 0 {
		t.Fatal("nothing scored")
	}
	if n := len(lastRates(f, orgA, node1, "sermon_anomaly_rate_ratio")); n != 5 {
		t.Errorf("%d rates written", n)
	}
}

// M4-31: an organization that comes later still gets its share of the instance: a first one took
// it all, the second gets half at once, and the first gives its surplus back at its next listing.
func TestWorker_SharesTheInstanceFairly(t *testing.T) {
	f := newFake()
	for i := range 6 {
		f.series[orgA] = append(f.series[orgA], cpu(int64(1+i), identity.NodeID(uuid.New()), "user"))
	}
	c := &clock{t: now0}
	w := worker(t, f, c, func(cfg *anomalies.Config) { cfg.PerInstance, cfg.Backfill = 4, 0 })
	w.Round(context.Background())
	scored := func(org tenant.OrgID) float64 {
		f.mu.Lock()
		defer f.mu.Unlock()
		last := map[identity.NodeID]float64{}
		var at time.Time
		for _, a := range f.rates[org] {
			if a.Metric == "sermon_anomaly_series_scored" && !a.Start.Before(at) {
				if a.Start.After(at) {
					last, at = map[identity.NodeID]float64{}, a.Start
				}
				last[a.Node] = a.Avg
			}
		}
		total := 0.0
		for _, v := range last {
			total += v
		}
		return total
	}
	if got := scored(orgA); got != 4 {
		t.Fatalf("alone, A scores %v series, want the instance's 4", got)
	}
	f.mu.Lock()
	f.series[orgB] = []anomalies.Series{cpu(11, node1, "user"), cpu(12, node1, "system"), cpu(13, node2, "user")}
	f.mu.Unlock()
	c.set(now0.Add(time.Minute))
	w.Round(context.Background())
	if got := scored(orgB); got != 2 {
		t.Errorf("B scores %v series, want its share of 2 (before: none, A held all)", got)
	}
	c.set(now0.Add(12 * time.Minute))
	w.Round(context.Background())
	if a, b := scored(orgA), scored(orgB); a != 2 || b != 2 {
		t.Errorf("after A listed again: A %v, B %v, want 2 and 2", a, b)
	}
}

// M4-31: an instance with no series of an organization doesn't hold its lease – another one with
// room takes it.
func TestWorker_GivesUpAnOrganizationWithoutSeries(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	f.series[orgB] = []anomalies.Series{}
	w := worker(t, f, &clock{t: now0}, func(cfg *anomalies.Config) { cfg.Backfill = 0 })
	w.Round(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.released, []tenant.OrgID{orgB}) || f.holders[orgA] != "a" {
		t.Errorf("released %v, holders %v", f.released, f.holders)
	}
}

// M4-31: the writes check the lease in their transaction: after a takeover (a round longer than
// the lease) the old holder writes nothing more and starts anew.
func TestWorker_WritesNothingAfterItsLeaseWasTaken(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	c := &clock{t: now0}
	w := worker(t, f, c, func(cfg *anomalies.Config) { cfg.Backfill = 0 })
	w.Round(context.Background())
	f.mu.Lock()
	before := len(f.rates[orgA])
	checks := f.checks
	f.onSave = func(org tenant.OrgID) { f.holders[org] = "b" }
	f.mu.Unlock()
	c.set(now0.Add(time.Minute))
	w.Round(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checks == checks {
		t.Fatal("a chunk without notable minutes wrote its rates without a lease check")
	}
	if len(f.rates[orgA]) != before {
		t.Errorf("%d rates written after the lease was taken", len(f.rates[orgA])-before)
	}
	if w.Stats().LeasesLost.Load() != 1 || w.Stats().Errors.Load() != 0 {
		t.Errorf("leases lost %d, errors %d", w.Stats().LeasesLost.Load(), w.Stats().Errors.Load())
	}
}

// M5-32: a server that stops gives its leases up, so the instance after a restart or an upgrade
// takes over at once instead of after the lease ran out (up to 3 minutes without scores).
func TestWorker_ReleasesItsLeasesWhenStopped(t *testing.T) {
	f := newFake()
	f.series[orgA] = []anomalies.Series{cpu(1, node1, "user")}
	f.series[orgB] = []anomalies.Series{cpu(2, node2, "user")}
	w := worker(t, f, &clock{t: now0}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		held := len(f.holders)
		f.mu.Unlock()
		if held == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker holds %d leases", held)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.holders) != 0 {
		t.Errorf("leases still held after stopping: %v", f.holders)
	}
	// The next instance takes over at once.
	next := worker(t, f, &clock{t: now0}, func(c *anomalies.Config) { c.Holder = "b" })
	f.mu.Unlock()
	next.Round(context.Background())
	f.mu.Lock()
	if f.holders[orgA] != "b" || f.holders[orgB] != "b" {
		t.Errorf("the next instance holds %v", f.holders)
	}
}
