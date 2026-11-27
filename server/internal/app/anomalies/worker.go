// Package anomalies runs the anomaly detection (KONZEPT §13, ADR 0037): a worker that scores the
// candidate series of every organization each minute and the use cases that read the results.
package anomalies

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/anomaly"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Learning is how long a node or a new series learns before anomalies count (KONZEPT §13).
const Learning = anomaly.Learning

// Series is a candidate series of an organization.
type Series struct {
	ID       int64
	Node     identity.NodeID
	Metric   string
	Labels   map[string]string
	Created  time.Time
	Learning time.Time // since when its node learns
}

// Point is one minute of a series (from metrics_1m).
type Point struct {
	Series int64
	T      time.Time
	V      float64
}

// Notable is a minute worth storing (score ≥ 3 or anomalous).
type Notable struct {
	Series    int64
	T         time.Time
	Score     float64
	Anomalous bool
}

// Store is the worker's port to the database.
type Store interface {
	// Organizations returns the organizations to look after (not locked).
	Organizations(ctx context.Context) ([]tenant.OrgID, error)
	// Lease takes or renews the organization's lease for leaseFor from the database's time; false
	// if another holds it.
	Lease(ctx context.Context, org tenant.OrgID, holder string, leaseFor time.Duration) (bool, error)
	// Release gives the holder's lease up.
	Release(ctx context.Context, org tenant.OrgID, holder string) error
	// Series returns the series of the metrics with data since since, at most perNode of a node
	// (the metrics' order first) and limit in all.
	Series(ctx context.Context, org tenant.OrgID, metrics []string, since time.Time, perNode, limit int) ([]Series, error)
	// Points returns the 1-min values of the series in [from, to).
	Points(ctx context.Context, org tenant.OrgID, ids []int64, from, to time.Time) ([]Point, error)
	// Baselines returns the stored baselines of an hour of the week.
	Baselines(ctx context.Context, org tenant.OrgID, hour int) (map[int64]anomaly.Baseline, error)
	// SaveBaselines stores the baselines of an hour of the week, computed at at, in place of all
	// it had: the hour's age is that of its oldest row. ErrLeaseLost if holder's lease is gone.
	SaveBaselines(ctx context.Context, org tenant.OrgID, holder string, hour int, at time.Time, baselines map[int64]anomaly.Baseline) error
	// BaselineAges returns when each stored hour of the week was computed.
	BaselineAges(ctx context.Context, org tenant.OrgID) (map[int]time.Time, error)
	// SaveNotable stores notable minutes (a minute stored before stays), none at all only checks
	// the lease: ErrLeaseLost if holder's lease is gone.
	SaveNotable(ctx context.Context, org tenant.OrgID, holder string, notable []Notable) error
}

// ErrLeaseLost means another instance took the organization over: the old holder writes nothing
// more (M4-31).
var ErrLeaseLost = errors.New("the lease of the organization was lost")

// RateWriter stores the derived series of the nodes (the metric store).
type RateWriter interface {
	Write(ctx context.Context, org tenant.OrgID, aggregates []metrics.Aggregate, persisted map[identity.NodeID]uint64) error
}

// Config are the worker's timing and limits (ADR 0037).
type Config struct {
	Holder   string
	Interval time.Duration
	LeaseFor time.Duration
	// Budget bounds a round; what isn't done, the next round continues.
	Budget time.Duration
	// Lag is how long metrics_1m takes to hold a minute.
	Lag time.Duration
	// Warmup is the history scored after a takeover to warm the short-term baselines; Output is
	// how far back the first round after it writes results (older minutes only warm up). From
	// then on every minute is written, however long the organization waited for its turn.
	Warmup, Output time.Duration
	// Chunk is the most minutes a step scores at once.
	Chunk time.Duration
	// Refresh is how often the series are listed again.
	Refresh time.Duration
	// PerNode, PerOrg and PerInstance bound the scored series.
	PerNode, PerOrg, PerInstance int
	// Backfill is how many missing or stale hours of the week a step computes.
	Backfill int
	Now      func() time.Time
	Logger   *slog.Logger
}

// DefaultConfig returns the defaults for holder.
func DefaultConfig(holder string, logger *slog.Logger) Config {
	return Config{
		Holder: holder, Interval: time.Minute, LeaseFor: 3 * time.Minute, Budget: 20 * time.Second,
		Lag: 3 * time.Minute, Warmup: 6 * time.Hour, Output: 5 * time.Minute, Chunk: time.Hour,
		Refresh: 10 * time.Minute, PerNode: 64, PerOrg: 4000, PerInstance: 20000, Backfill: 4,
		Now: time.Now, Logger: logger,
	}
}

// Stats count the worker's work.
type Stats struct {
	Rounds, Scored, Notable, Skipped, Baselines, LeasesLost, Overruns, Errors atomic.Uint64
}

type seriesState struct {
	Series
	spec    metric.AnomalySpec
	state   anomaly.State
	last    float64 // a counter's value of the previous minute
	hasLast bool
}

type orgState struct {
	series []*seriesState
	byID   map[int64]*seriesState
	listed time.Time
	done   time.Time // minutes before this are scored
	// writeFrom: results are written from this minute on, older ones only warm up (the takeover's
	// Output; review M4-30: not the last Output of every round, a wait would lose minutes).
	writeFrom time.Time
	hour      time.Time // the hour whose baselines are loaded
	current   map[int64]anomaly.Baseline
	// tried is when a missing hour of the week was last computed: without data it stays missing
	// and is tried again an hour later, not every minute.
	tried map[int]time.Time
}

// Worker scores the candidate series of the organizations whose lease it holds.
type Worker struct {
	store  Store
	rates  RateWriter
	config Config
	stats  Stats
	orgs   map[tenant.OrgID]*orgState
	turn   int
	// cut is the organization whose step ran out of the budget last round (cutOff): the next
	// round starts after it, so one big organization can't take every round (review M4-30).
	cut    tenant.OrgID
	cutOff bool
	names  []string
	order  map[string]int
}

// NewWorker returns the worker.
func NewWorker(store Store, rates RateWriter, config Config) *Worker {
	w := &Worker{store: store, rates: rates, config: config, orgs: map[tenant.OrgID]*orgState{}, order: map[string]int{}}
	for i, d := range metric.Candidates() {
		w.names = append(w.names, d.Name)
		w.order[d.Name] = i
	}
	return w
}

// Stats returns the counters.
func (w *Worker) Stats() *Stats { return &w.stats }

// Run runs a round every interval until ctx ends; rounds never overlap.
func (w *Worker) Run(ctx context.Context) error {
	tick := time.NewTicker(w.config.Interval)
	defer tick.Stop()
	for {
		w.Round(ctx)
		select {
		case <-ctx.Done():
			w.releaseAll(context.WithoutCancel(ctx))
			return nil
		case <-tick.C:
		}
	}
}

// releaseTimeout bounds giving the leases up when the server stops.
const releaseTimeout = 5 * time.Second

// releaseAll gives up the leases this instance holds: after a restart or an upgrade the next
// instance takes the organizations over at once, not only once the leases ran out.
func (w *Worker) releaseAll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	for org := range w.orgs {
		if err := w.store.Release(ctx, org, w.config.Holder); err != nil {
			w.config.Logger.WarnContext(ctx, "anomalies: release lease failed", "org", org.String(), "error", err)
		}
		delete(w.orgs, org)
	}
}

var errBudget = errors.New("round budget spent")

// Round scores what is ready of every organization within the budget: the first one changes each
// round, after a round that ran out of it the one after the organization it ran out in.
func (w *Worker) Round(ctx context.Context) {
	w.stats.Rounds.Add(1)
	started := w.config.Now()
	orgs, err := w.store.Organizations(ctx)
	if err != nil {
		w.stats.Errors.Add(1)
		w.config.Logger.ErrorContext(ctx, "anomalies: list organizations failed", "error", err)
		return
	}
	for org := range w.orgs {
		if !slices.Contains(orgs, org) {
			delete(w.orgs, org)
		}
	}
	if len(orgs) == 0 {
		return
	}
	w.turn = (w.turn + 1) % len(orgs)
	if i := slices.Index(orgs, w.cut); w.cutOff && i >= 0 {
		w.turn = (i + 1) % len(orgs)
	}
	w.cutOff = false
	deadline := started.Add(w.config.Budget)
	for i := range orgs {
		org := orgs[(w.turn+i)%len(orgs)]
		err := w.step(ctx, org, deadline)
		switch {
		case errors.Is(err, ErrLeaseLost):
			// Taken over meanwhile (a step longer than the lease): a takeover later warms up anew.
			w.stats.LeasesLost.Add(1)
			delete(w.orgs, org)
		case errors.Is(err, errBudget):
			w.stats.Overruns.Add(1)
			w.cut, w.cutOff = org, true
			return
		case err != nil:
			w.stats.Errors.Add(1)
			w.config.Logger.ErrorContext(ctx, "anomalies: step failed", "org", org.String(), "error", err)
		}
	}
}

// instanceSeries counts the series this instance scores, without org.
func (w *Worker) instanceSeries(org tenant.OrgID) int {
	n := 0
	for o, s := range w.orgs {
		if o != org {
			n += len(s.series)
		}
	}
	return n
}

func (w *Worker) step(ctx context.Context, org tenant.OrgID, deadline time.Time) error {
	now := w.config.Now()
	if !now.Before(deadline) {
		return errBudget
	}
	held, err := w.store.Lease(ctx, org, w.config.Holder, w.config.LeaseFor)
	if err != nil {
		return fmt.Errorf("lease: %w", err)
	}
	if !held {
		// Another instance scores it; a takeover later warms up anew.
		if _, ok := w.orgs[org]; ok {
			w.stats.LeasesLost.Add(1)
			delete(w.orgs, org)
		}
		return nil
	}
	st := w.orgs[org]
	if st == nil {
		st = &orgState{
			done:      now.Truncate(time.Minute).Add(-w.config.Warmup),
			writeFrom: now.Add(-w.config.Lag - w.config.Output).Truncate(time.Minute),
		}
		w.orgs[org] = st
	}
	if st.series == nil || now.Sub(st.listed) >= w.config.Refresh {
		if err := w.list(ctx, org, st, now); err != nil {
			return err
		}
	}
	if len(st.series) == 0 {
		// Nothing of it to score here (none or no room left): another instance may take it.
		delete(w.orgs, org)
		if err := w.store.Release(ctx, org, w.config.Holder); err != nil {
			return fmt.Errorf("release: %w", err)
		}
		return nil
	}
	// Missing hours first: the warm-up after a takeover scores against them at once (a
	// baseline arriving later would start the short-term baselines anew).
	if err := w.backfill(ctx, org, st, now, deadline); err != nil {
		return err
	}
	ready := now.Add(-w.config.Lag).Truncate(time.Minute)
	for st.done.Before(ready) {
		if !w.config.Now().Before(deadline) {
			return errBudget
		}
		// A chunk stays within an hour: one hour's baselines at a time.
		hour := st.done.Truncate(time.Hour)
		to := minTime(ready, minTime(st.done.Add(w.config.Chunk), hour.Add(time.Hour)))
		if err := w.score(ctx, org, st, st.done, to); err != nil {
			return err
		}
		if to.Equal(hour.Add(time.Hour)) {
			// The hour ended: its bucket learns it for next week.
			if err := w.recompute(ctx, org, st, hour, now); err != nil {
				return err
			}
		}
		st.done = to
	}
	return nil
}

// list chooses the scored series: candidates whose labels count, the catalog's order first, at
// most PerNode per node, PerOrg per organization and PerInstance in all. Of the instance an
// organization gets what the others left, at least an equal share of it (M4-31): one that came
// first can't keep the others out; it gives its surplus back at its next listing.
func (w *Worker) list(ctx context.Context, org tenant.OrgID, st *orgState, now time.Time) error {
	share := w.config.PerInstance / max(len(w.orgs), 1)
	limit := min(w.config.PerOrg, max(w.config.PerInstance-w.instanceSeries(org), share))
	// seen_at moves only every SeenResolution: an hour more than that finds every live series. Four
	// times the limits: the labels sort some out.
	found, err := w.store.Series(ctx, org, w.names, now.Add(-metrics.SeenResolution-time.Hour), w.config.PerNode*4, w.config.PerOrg*4)
	if err != nil {
		return fmt.Errorf("list series: %w", err)
	}
	slices.SortStableFunc(found, func(a, b Series) int {
		if c := a.Node.String(); c != b.Node.String() {
			if c < b.Node.String() {
				return -1
			}
			return 1
		}
		return w.order[a.Metric] - w.order[b.Metric]
	})
	perNode := map[identity.NodeID]int{}
	old := st.byID
	st.series, st.byID, st.listed = nil, map[int64]*seriesState{}, now
	skipped := 0
	for _, s := range found {
		d, ok := metric.Lookup(s.Metric)
		if !ok || d.Anomaly == nil || !d.Anomaly.Scores(s.Labels) {
			continue
		}
		if perNode[s.Node] >= w.config.PerNode || len(st.series) >= limit {
			skipped++
			continue
		}
		perNode[s.Node]++
		ss := old[s.ID]
		if ss == nil {
			ss = &seriesState{Series: s, spec: *d.Anomaly}
		}
		ss.Series = s
		st.series = append(st.series, ss)
		st.byID[s.ID] = ss
	}
	if skipped > 0 {
		w.stats.Skipped.Add(uint64(skipped))
	}
	return nil
}

// score scores the minutes [from, to) of one hour and writes those from st.writeFrom on.
func (w *Worker) score(ctx context.Context, org tenant.OrgID, st *orgState, from, to time.Time) error {
	if hour := from.Truncate(time.Hour); !hour.Equal(st.hour) {
		b, err := w.store.Baselines(ctx, org, anomaly.HourOfWeek(from))
		if err != nil {
			return fmt.Errorf("read baselines: %w", err)
		}
		st.hour, st.current = hour, b
	}
	ids := make([]int64, len(st.series))
	for i, s := range st.series {
		ids[i] = s.ID
	}
	points, err := w.store.Points(ctx, org, ids, from, to)
	if err != nil {
		return fmt.Errorf("read points: %w", err)
	}
	// By the minute's Unix time: the store answers in UTC, the clock may run in time.Local – equal
	// instants, but other map keys (M4-28).
	values := map[int64]map[int64]float64{}
	for _, p := range points {
		if values[p.Series] == nil {
			values[p.Series] = map[int64]float64{}
		}
		values[p.Series][p.T.Unix()] = p.V
	}
	// After a takeover only the last Output of the warm-up: nothing older is written twice.
	outputFrom := st.writeFrom
	var notable []Notable
	var aggregates []metrics.Aggregate
	for m := from; m.Before(to); m = m.Add(time.Minute) {
		scored := map[identity.NodeID]int{}
		anomalous := map[identity.NodeID]int{}
		for _, s := range st.series {
			v, ok := values[s.ID][m.Unix()]
			if !ok {
				continue
			}
			if s.spec.Counter {
				prev, had := s.last, s.hasLast
				s.last, s.hasLast = v, true
				if !had || v < prev {
					continue // the first value or a restart of the agent
				}
				v -= prev
			}
			baseline, has := st.current[s.ID]
			verdict := anomaly.Robust{Floor: s.spec.Floor}.Score(&s.state, m, v, baseline, has && baseline.N >= anomaly.MinPoints)
			w.stats.Scored.Add(1)
			if m.Before(outputFrom) {
				continue // warming up
			}
			if learning(s.Series, m) {
				// Learning: the node's rate is written, as 0.
				scored[s.Node] += 0
				continue
			}
			scored[s.Node]++
			if verdict.Anomalous {
				anomalous[s.Node]++
			}
			if verdict.Notable() {
				notable = append(notable, Notable{Series: s.ID, T: m, Score: verdict.Score, Anomalous: verdict.Anomalous})
			}
		}
		for node, n := range scored {
			ratio := 0.0
			if n > 0 {
				ratio = float64(anomalous[node]) / float64(n) * 1e6
			}
			aggregates = append(aggregates,
				metrics.Aggregate{Node: node, Metric: "sermon_anomaly_rate_ratio", Labels: metrics.Labels{}, Start: m, Avg: ratio, Min: ratio, Max: ratio},
				metrics.Aggregate{Node: node, Metric: "sermon_anomaly_series_scored", Labels: metrics.Labels{}, Start: m, Avg: float64(n), Min: float64(n), Max: float64(n)})
		}
	}
	// Also without notable minutes: it checks the lease before the rates are written.
	if err := w.store.SaveNotable(ctx, org, w.config.Holder, notable); err != nil {
		return fmt.Errorf("save notable minutes: %w", err)
	}
	w.stats.Notable.Add(uint64(len(notable)))
	if len(aggregates) > 0 {
		if err := w.rates.Write(ctx, org, aggregates, nil); err != nil {
			return fmt.Errorf("write anomaly rates: %w", err)
		}
	}
	return nil
}

// learning reports whether the series still learns at m: its node or the series itself is
// younger than Learning.
func learning(s Series, m time.Time) bool {
	return m.Before(s.Learning.Add(Learning)) || m.Before(s.Created.Add(Learning))
}

// recompute computes the bucket of hour from the same hour of the last four weeks, hour itself
// included: it is the baseline of that hour of the week from next week on.
func (w *Worker) recompute(ctx context.Context, org tenant.OrgID, st *orgState, hour, now time.Time) error {
	ids := make([]int64, len(st.series))
	for i, s := range st.series {
		ids[i] = s.ID
	}
	values := map[int64][]float64{}
	for week := range 4 {
		from := hour.Add(-time.Duration(week) * 7 * 24 * time.Hour)
		points, err := w.store.Points(ctx, org, ids, from, from.Add(time.Hour))
		if err != nil {
			return fmt.Errorf("read points for the baselines: %w", err)
		}
		// Counters by their increase, like the scores.
		last := map[int64]float64{}
		slices.SortFunc(points, func(a, b Point) int { return a.T.Compare(b.T) })
		for _, p := range points {
			s := st.byID[p.Series]
			if s == nil {
				continue
			}
			v := p.V
			if s.spec.Counter {
				prev, had := last[p.Series]
				last[p.Series] = p.V
				if !had || p.V < prev {
					continue
				}
				v -= prev
			}
			values[p.Series] = append(values[p.Series], v)
		}
	}
	baselines := map[int64]anomaly.Baseline{}
	for id, vs := range values {
		if b, ok := anomaly.NewBaseline(vs); ok {
			baselines[id] = b
		}
	}
	bucket := anomaly.HourOfWeek(hour)
	if err := w.store.SaveBaselines(ctx, org, w.config.Holder, bucket, now, baselines); err != nil {
		return fmt.Errorf("save baselines: %w", err)
	}
	w.stats.Baselines.Add(1)
	if anomaly.HourOfWeek(st.hour) == bucket {
		// The loaded hour changed under us (a backfill of the current bucket).
		st.hour = time.Time{}
	}
	return nil
}

// week is how far back the occurrence of an hour lies whose bucket scores it.
const week = 7 * 24 * time.Hour

// retryMissing is how long a missing hour of the week waits for its next attempt.
const retryMissing = time.Hour

// backfill computes the hours of the week that are missing or older than a week and a day, from
// their last occurrence before now: first, and all of them, those the minutes from st.done to now
// are scored against (after a takeover the six hours of the warm-up); then up to Backfill others,
// from the current hour on – the ones needed next first. A missing hour is tried at most once
// per retryMissing.
func (w *Worker) backfill(ctx context.Context, org tenant.OrgID, st *orgState, now, deadline time.Time) error {
	ages, err := w.store.BaselineAges(ctx, org)
	if err != nil {
		return fmt.Errorf("read baseline ages: %w", err)
	}
	if st.tried == nil {
		st.tried = map[int]time.Time{}
	}
	due := func(hour time.Time) bool {
		bucket := anomaly.HourOfWeek(hour)
		if at, ok := ages[bucket]; ok && now.Sub(at) < week+24*time.Hour {
			return false
		}
		at, ok := st.tried[bucket]
		return !ok || now.Sub(at) >= retryMissing
	}
	compute := func(hour time.Time) error {
		if !w.config.Now().Before(deadline) {
			return errBudget
		}
		st.tried[anomaly.HourOfWeek(hour)] = now
		return w.recompute(ctx, org, st, hour, now)
	}
	current := now.Truncate(time.Hour)
	for hour := st.done.Truncate(time.Hour); !hour.After(current); hour = hour.Add(time.Hour) {
		if due(hour.Add(-week)) {
			if err := compute(hour.Add(-week)); err != nil {
				return err
			}
		}
	}
	done := 0
	for back := 168; back >= 1 && done < w.config.Backfill; back-- {
		hour := current.Add(-time.Duration(back) * time.Hour)
		if !due(hour) {
			continue
		}
		if err := compute(hour); err != nil {
			return err
		}
		done++
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
