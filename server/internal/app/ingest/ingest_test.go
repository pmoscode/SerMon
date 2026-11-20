package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	group = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	node  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	t0    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

type fakeNodes struct {
	connected []ingest.HostFacts
	seen      []time.Time
}

func (f *fakeNodes) Connected(_ context.Context, o tenant.OrgID, _ identity.NodeID, facts ingest.HostFacts, _ string, _ time.Time) (tenant.GroupID, error) {
	if o != org {
		return tenant.GroupID{}, errors.New("unknown node")
	}
	f.connected = append(f.connected, facts)
	return group, nil
}

func (f *fakeNodes) Seen(_ context.Context, _ tenant.OrgID, _ identity.NodeID, at time.Time) error {
	f.seen = append(f.seen, at)
	return nil
}

// failingStore fails writes while fail is set.
type failingStore struct {
	*memstore.Store
	fail   bool
	counts atomic.Int32 // OrgSeriesCount calls
}

func (f *failingStore) OrgSeriesCount(ctx context.Context, o tenant.OrgID, since time.Time) (int, error) {
	f.counts.Add(1)
	return f.Store.OrgSeriesCount(ctx, o, since)
}

func (f *failingStore) Write(ctx context.Context, o tenant.OrgID, a []metrics.Aggregate, p map[identity.NodeID]uint64) error {
	if f.fail {
		return errors.New("database down")
	}
	return f.Store.Write(ctx, o, a, p)
}

type world struct {
	store *failingStore
	ring  *metrics.Ring
	nodes *fakeNodes
	clock *clock
	svc   *ingest.Service
}

func newWorld(t *testing.T, config ingest.Config) *world {
	t.Helper()
	store := &failingStore{Store: memstore.New()}
	store.AddNode(org, group, node)
	w := &world{store: store, ring: metrics.NewRing(), nodes: &fakeNodes{}, clock: &clock{now: t0}}
	w.svc = ingest.NewService(store, w.ring, w.nodes, config, w.clock.Now, slog.New(slog.DiscardHandler))
	return w
}

func (w *world) open(t *testing.T) *ingest.Session {
	t.Helper()
	session, err := w.svc.Open(context.Background(), org, node, ingest.HostFacts{Hostname: "web-01"}, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func batch(seq uint64, at time.Time, value float64) ingest.Batch {
	return ingest.Batch{Seq: seq, CollectedAt: at, Samples: []ingest.Sample{{Metric: "sermon_load_average", Labels: metrics.Labels{"period": "1m"}, Value: value}}}
}

// send delivers one batch per second starting at seq/at and returns the last seq.
func send(t *testing.T, w *world, s *ingest.Session, firstSeq uint64, start time.Time, values ...float64) uint64 {
	t.Helper()
	seq := firstSeq
	for i, v := range values {
		at := start.Add(time.Duration(i) * time.Second)
		w.clock.Set(at.Add(200 * time.Millisecond))
		seq = firstSeq + uint64(i)
		if response := s.Accept(context.Background(), batch(seq, at, v)); response.Ack != 0 {
			t.Fatalf("batch %d acknowledged before it was stored", seq)
		}
	}
	return seq
}

func TestIngest_AggregatesTenSecondWindowsAndAcknowledgesAfterTheWrite(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	last := send(t, w, s, 100, t0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9) // window 12:00:00

	ack, err := s.Flush(context.Background())
	if err != nil || ack != 0 || len(w.store.Aggregates()) != 0 {
		t.Fatalf("Flush() = %d, %v with %d windows; the open window must not be written", ack, err, len(w.store.Aggregates()))
	}

	send(t, w, s, last+1, t0.Add(10*time.Second), 42) // first sample of the next window closes the first
	ack, err = s.Flush(context.Background())

	if err != nil || ack != last {
		t.Fatalf("Flush() = %d, %v; want ack %d (the last batch of the complete window)", ack, err, last)
	}
	aggregates := w.store.Aggregates()
	if len(aggregates) != 1 {
		t.Fatalf("aggregates = %+v", aggregates)
	}
	a := aggregates[0]
	if !a.Start.Equal(t0) || a.Avg != 4.5 || a.Min != 0 || a.Max != 9 || a.Node != node || a.Labels["period"] != "1m" {
		t.Errorf("aggregate = %+v", a)
	}
	if seq, _ := w.store.PersistedSeq(context.Background(), org, node); seq != last {
		t.Errorf("persisted seq = %d, want %d", seq, last)
	}
	if again, _ := s.Flush(context.Background()); again != 0 {
		t.Errorf("second Flush() acknowledged %d again", again)
	}
}

func TestIngest_DuplicatesAfterAReconnectAreAcknowledgedButNotStored(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	first := w.open(t)
	last := send(t, w, first, 1, t0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	if _, err := first.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored := len(w.store.Aggregates())

	// The agent reconnects and resends everything it has no ack for.
	second := w.open(t)
	response := second.Accept(context.Background(), batch(5, t0.Add(4*time.Second), 5))

	if response.Ack != last-1 || second.Persisted() != last-1 {
		t.Errorf("Accept(duplicate) ack = %d, persisted %d; want %d", response.Ack, second.Persisted(), last-1)
	}
	if _, err := second.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(w.store.Aggregates()); got != stored {
		t.Errorf("duplicate stored again: %d aggregates, want %d", got, stored)
	}
	if w.svc.Stats().Duplicates.Load() != 1 {
		t.Errorf("duplicates = %d", w.svc.Stats().Duplicates.Load())
	}
}

func TestIngest_OpenWindowsAreLeftToTheResendWhenTheSessionEnds(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	send(t, w, s, 1, t0, 1, 2, 3)

	s.Close(context.Background())

	if len(w.store.Aggregates()) != 0 {
		t.Errorf("partial window written: %+v", w.store.Aggregates())
	}
	if seq, _ := w.store.PersistedSeq(context.Background(), org, node); seq != 0 {
		t.Errorf("persisted seq = %d; the batches of the open window must come again", seq)
	}
	// The resend after the reconnect completes the window: nothing is lost or doubled.
	again := w.open(t)
	send(t, w, again, 1, t0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	if _, err := again.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := w.store.Aggregates(); len(a) != 1 || a[0].Avg != 5.5 {
		t.Errorf("aggregates = %+v, want one complete window with avg 5.5", a)
	}
}

func TestIngest_LateFutureAndAncientSamplesAreDroppedButAcknowledged(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	last := send(t, w, s, 1, t0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := w.clock.Now()

	s.Accept(context.Background(), batch(last+1, t0.Add(3*time.Second), 99))  // window already written
	s.Accept(context.Background(), batch(last+2, now.Add(time.Hour), 99))     // agent clock far ahead
	s.Accept(context.Background(), batch(last+3, now.Add(-48*time.Hour), 99)) // older than the retention
	s.Accept(context.Background(), batch(last+4, t0.Add(20*time.Second), 12)) // normal again
	ack, err := s.Flush(context.Background())

	if err != nil || ack != last+3 {
		t.Fatalf("Flush() = %d, %v; want %d (dropped batches are acknowledged)", ack, err, last+3)
	}
	for _, a := range w.store.Aggregates() {
		if a.Max == 99 {
			t.Errorf("dropped sample stored: %+v", a)
		}
	}
	stats := w.svc.Stats()
	if stats.Late.Load() != 1 || stats.Rejected.Load() != 2 {
		t.Errorf("late %d, rejected %d; want 1 and 2", stats.Late.Load(), stats.Rejected.Load())
	}
}

func TestIngest_AWriteErrorAcknowledgesNothingAndRetries(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	last := send(t, w, s, 1, t0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	w.store.fail = true

	if ack, err := s.Flush(context.Background()); err == nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; want an error and no ack", ack, err)
	}
	w.store.fail = false
	if ack, err := s.Flush(context.Background()); err != nil || ack != last-1 {
		t.Fatalf("retry Flush() = %d, %v; want %d", ack, err, last-1)
	}
}

func TestIngest_FillsTheRingWithOneSecondValues(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	send(t, w, s, 1, t0, 1, 2, 3)
	scope, _ := tenant.ForAllGroups(org)

	series := w.ring.Query(scope, metrics.Query{Metric: "sermon_load_average", Nodes: []identity.NodeID{node}, From: t0, To: t0.Add(time.Minute), Step: time.Second})

	if len(series) != 1 || len(series[0].Points) != 3 || series[0].Points[2].Avg != 3 {
		t.Errorf("ring = %+v", series)
	}
}

func TestIngest_ThrottlesOverQuotaAndLiftsWhenCalm(t *testing.T) {
	config := ingest.DefaultConfig()
	config.SampleRate, config.SampleBurst = 10, 20
	w := newWorld(t, config)
	s := w.open(t)
	big := ingest.Batch{Seq: 1, CollectedAt: t0, Samples: make([]ingest.Sample, 25)}
	for i := range big.Samples {
		big.Samples[i] = ingest.Sample{Metric: "sermon_disk_reads_per_second", Labels: metrics.Labels{"device": string(rune('a' + i))}, Value: 1}
	}
	w.clock.Set(t0)

	response := s.Accept(context.Background(), big)
	if response.Throttle == nil || *response.Throttle != config.ThrottleInterval {
		t.Fatalf("over quota: throttle = %v, want %v", response.Throttle, config.ThrottleInterval)
	}
	w.clock.Set(t0.Add(3 * time.Second)) // 30 tokens refilled, capped at 20
	response = s.Accept(context.Background(), batch(2, t0.Add(time.Second), 1))
	if response.Throttle == nil || *response.Throttle != 0 {
		t.Fatalf("calm again: throttle = %v, want lifted (0)", response.Throttle)
	}
}

func TestIngest_DropsNewSeriesBeyondTheLimit(t *testing.T) {
	config := ingest.DefaultConfig()
	config.SeriesLimit = 2
	w := newWorld(t, config)
	s := w.open(t)
	samples := []ingest.Sample{
		{Metric: "sermon_tcp_established", Value: 1}, {Metric: "sermon_tcp_time_wait", Value: 1}, {Metric: "sermon_system_zombies", Value: 1},
	}
	s.Accept(context.Background(), ingest.Batch{Seq: 1, CollectedAt: t0, Samples: samples})
	s.Accept(context.Background(), batch(2, t0.Add(10*time.Second), 1))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	metricsStored := map[string]bool{}
	for _, a := range w.store.Aggregates() {
		metricsStored[a.Metric] = true
	}
	if len(metricsStored) != 2 || metricsStored["sermon_system_zombies"] || w.svc.Stats().DroppedSeries.Load() == 0 {
		t.Errorf("stored metrics %v, dropped %d", metricsStored, w.svc.Stats().DroppedSeries.Load())
	}
}

func TestIngest_RecordsTheHelloAndContactAtMostEveryInterval(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	if len(w.nodes.connected) != 1 || w.nodes.connected[0].Hostname != "web-01" {
		t.Fatalf("Connected() calls = %+v", w.nodes.connected)
	}
	for second := range 61 {
		w.clock.Set(t0.Add(time.Duration(second) * time.Second))
		s.Heartbeat(context.Background())
	}
	if len(w.nodes.seen) != 2 {
		t.Errorf("Seen() calls = %v, want 2 (at 30 s and 60 s)", w.nodes.seen)
	}
}

func TestIngest_OpenFailsForUnknownNodes(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	_, err := w.svc.Open(context.Background(), org, identity.NodeID(uuid.New()), ingest.HostFacts{}, "1")
	if !errors.Is(err, ingest.ErrOpen) {
		t.Errorf("Open() error = %v, want ErrOpen", err)
	}
}

func TestOnline_AfterSixtySecondsWithoutContactANodeIsOffline(t *testing.T) {
	seen := t0
	if !ingest.Online(&seen, t0.Add(59*time.Second)) || ingest.Online(&seen, t0.Add(60*time.Second)) || ingest.Online(nil, t0) {
		t.Error("online window wrong")
	}
}

func TestIngest_DropsUnknownMetricsAndUnexpectedLabels(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	samples := []ingest.Sample{
		{Metric: "sermon_load_average", Labels: metrics.Labels{"period": "1m"}, Value: 1},                // known
		{Metric: "sermon_made_up", Value: 1},                                                             // unknown
		{Metric: "sermon_load_average", Labels: metrics.Labels{"period": "1m", "pid": "4242"}, Value: 1}, // extra label
		{Metric: "sermon_load_average", Value: 1},                                                        // missing label
	}
	s.Accept(context.Background(), ingest.Batch{Seq: 1, CollectedAt: t0, Samples: samples})
	s.Accept(context.Background(), batch(2, t0.Add(10*time.Second), 1))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	if a := w.store.Aggregates(); len(a) != 1 || len(a[0].Labels) != 1 {
		t.Errorf("stored %+v, want only the known series", a)
	}
	if got := w.svc.Stats().Unknown.Load(); got != 3 {
		t.Errorf("unknown = %d, want 3", got)
	}
}

// An agent that ignores Throttle (compromised or broken) must not keep writing at full speed:
// beyond the slack for batches in flight its samples are dropped, the batches are still
// acknowledged.
func TestIngest_DropsSamplesOfAnAgentIgnoringTheThrottle(t *testing.T) {
	config := ingest.DefaultConfig()
	config.SampleRate, config.SampleBurst, config.ThrottleSlack = 10, 20, 20
	w := newWorld(t, config)
	s := w.open(t)
	w.clock.Set(t0)
	flood := func(seq uint64) ingest.Batch {
		b := ingest.Batch{Seq: seq, CollectedAt: t0, Samples: make([]ingest.Sample, 25)}
		for i := range b.Samples {
			b.Samples[i] = ingest.Sample{Metric: "sermon_disk_reads_per_second", Labels: metrics.Labels{"device": string(rune('a' + i))}, Value: 1}
		}
		return b
	}

	s.Accept(context.Background(), flood(1)) // over quota: throttled, still stored
	before := w.svc.Stats().Samples.Load()
	s.Accept(context.Background(), flood(2)) // ignores the throttle: beyond the slack
	s.Accept(context.Background(), flood(3))

	if stored := w.svc.Stats().Samples.Load() - before; stored != 0 {
		t.Errorf("%d samples stored while the agent ignored the throttle, want 0", stored)
	}
	if rejected := w.svc.Stats().Rejected.Load(); rejected != 50 {
		t.Errorf("rejected = %d, want 50", rejected)
	}
	// Calm again: after the refill the agent is admitted; the dropped batches are acknowledged.
	w.clock.Set(t0.Add(10 * time.Second))
	s.Accept(context.Background(), batch(4, t0.Add(10*time.Second), 1))
	if stored := w.svc.Stats().Samples.Load() - before; stored != 1 {
		t.Errorf("after the pause %d samples stored, want 1", stored)
	}
	if ack, err := s.Flush(context.Background()); err != nil || ack < 3 {
		t.Errorf("Flush() = %d, %v; dropped batches must still be acknowledged", ack, err)
	}
}

// An honest agent replaying its buffer has up to 64 full batches in flight when Throttle arrives;
// the default slack keeps all of them.
func TestIngest_DefaultSlackKeepsTheBatchesInFlight(t *testing.T) {
	config := ingest.DefaultConfig()
	w := newWorld(t, config)
	s := w.open(t)
	w.clock.Set(t0)
	samples := make([]ingest.Sample, config.SeriesLimit)
	for i := range samples {
		samples[i] = ingest.Sample{Metric: "sermon_disk_reads_per_second", Labels: metrics.Labels{"device": fmt.Sprintf("d%04d", i)}, Value: 1}
	}
	for seq := uint64(1); seq <= 64+uint64(config.SampleBurst)/uint64(config.SeriesLimit); seq++ {
		s.Accept(context.Background(), ingest.Batch{Seq: seq, CollectedAt: t0, Samples: samples})
	}
	if rejected := w.svc.Stats().Rejected.Load(); rejected != 0 {
		t.Errorf("%d samples of batches in flight rejected, want 0", rejected)
	}
}

func seriesSamples(names ...string) []ingest.Sample {
	out := make([]ingest.Sample, len(names))
	for i, n := range names {
		out[i] = ingest.Sample{Metric: "sermon_disk_reads_per_second", Labels: metrics.Labels{"device": n}, Value: 1}
	}
	return out
}

func storedDevices(w *world, of identity.NodeID) map[string]bool {
	out := map[string]bool{}
	for _, a := range w.store.Aggregates() {
		if a.Node == of {
			out[a.Labels["device"]] = true
		}
	}
	return out
}

// A compromised agent must not get a fresh series budget by reconnecting (M1-36): the limit
// counts the node's series stored in the last ActiveSeriesWindow.
func TestIngest_SeriesLimitHoldsAcrossReconnects(t *testing.T) {
	config := ingest.DefaultConfig()
	config.SeriesLimit = 2
	w := newWorld(t, config)
	first := w.open(t)
	first.Accept(context.Background(), ingest.Batch{Seq: 1, CollectedAt: t0, Samples: seriesSamples("a", "b")})
	first.Accept(context.Background(), ingest.Batch{Seq: 2, CollectedAt: t0.Add(10 * time.Second), Samples: seriesSamples("a")})
	if _, err := first.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	w.clock.Set(t0.Add(time.Minute))
	second := w.open(t)
	second.Accept(context.Background(), ingest.Batch{Seq: 3, CollectedAt: t0.Add(time.Minute), Samples: seriesSamples("c", "a", "b")})
	second.Accept(context.Background(), ingest.Batch{Seq: 4, CollectedAt: t0.Add(80 * time.Second), Samples: seriesSamples("a")})
	if _, err := second.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := storedDevices(w, node); got["c"] || !got["a"] || !got["b"] {
		t.Errorf("stored %v: the known series pass, a new one beyond the limit doesn't", got)
	}

	// Series without data for the window free their place.
	w.clock.Set(t0.Add(config.ActiveSeriesWindow + metrics.SeenResolution + time.Hour))
	later := w.clock.Now()
	third := w.open(t)
	third.Accept(context.Background(), ingest.Batch{Seq: 5, CollectedAt: later, Samples: seriesSamples("c")})
	third.Accept(context.Background(), ingest.Batch{Seq: 6, CollectedAt: later.Add(10 * time.Second), Samples: seriesSamples("c")})
	if _, err := third.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !storedDevices(w, node)["c"] {
		t.Error("after the window a new series is admitted again")
	}
}

// One organization can't fill the database with series of many nodes.
func TestIngest_LimitsTheSeriesOfAnOrganization(t *testing.T) {
	config := ingest.DefaultConfig()
	config.OrgSeriesLimit = 3
	w := newWorld(t, config)
	other := identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	w.store.AddNode(org, group, other)
	first := w.open(t)
	first.Accept(context.Background(), ingest.Batch{Seq: 1, CollectedAt: t0, Samples: seriesSamples("a", "b")})
	first.Accept(context.Background(), ingest.Batch{Seq: 2, CollectedAt: t0.Add(10 * time.Second), Samples: seriesSamples("a")})
	if _, err := first.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	second, err := w.svc.Open(context.Background(), org, other, ingest.HostFacts{Hostname: "web-02"}, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	second.Accept(context.Background(), ingest.Batch{Seq: 1, CollectedAt: t0, Samples: seriesSamples("x", "y")})
	second.Accept(context.Background(), ingest.Batch{Seq: 2, CollectedAt: t0.Add(10 * time.Second), Samples: seriesSamples("x")})
	if _, err := second.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := storedDevices(w, other); len(got) != 1 {
		t.Errorf("second node stored %v, want one series (org limit 3, two in use)", got)
	}
	if w.svc.Stats().DroppedSeries.Load() == 0 {
		t.Error("dropped series must be counted")
	}
}

// After a server restart every agent of an organization reconnects at once: the organization's
// series are counted once per OrgCountInterval, not once per session.
func TestIngest_CountsTheSeriesOfAnOrganizationOncePerInterval(t *testing.T) {
	config := ingest.DefaultConfig()
	w := newWorld(t, config)
	for range 3 {
		w.open(t)
	}
	if n := w.store.counts.Load(); n != 1 {
		t.Errorf("OrgSeriesCount called %d times for 3 sessions, want 1", n)
	}
	w.clock.Set(t0.Add(config.OrgCountInterval))
	w.open(t)
	if n := w.store.counts.Load(); n != 2 {
		t.Errorf("after the interval: %d calls, want 2", n)
	}
}
