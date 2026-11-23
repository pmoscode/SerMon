package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// fakeLogs records the written entries; fail makes the next writes fail with that error.
type fakeLogs struct {
	mu      sync.Mutex
	written []ingest.LogEntry
	nodes   []identity.NodeID
	orgs    []tenant.OrgID
	calls   int
	fail    error
}

func (f *fakeLogs) Write(_ context.Context, org tenant.OrgID, node identity.NodeID, entries []ingest.LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	f.written = append(f.written, entries...)
	for range entries {
		f.nodes = append(f.nodes, node)
		f.orgs = append(f.orgs, org)
	}
	return nil
}

func (f *fakeLogs) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.written))
	for _, e := range f.written {
		out = append(out, e.Message)
	}
	return out
}

func (f *fakeLogs) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func logEntry(at time.Time, message string) ingest.LogEntry {
	return ingest.LogEntry{At: at, Source: ingest.LogSourceJournal, Unit: "nginx.service", Severity: 4, Message: message}
}

func logBatch(seq uint64, entries ...ingest.LogEntry) ingest.Batch {
	return ingest.Batch{Seq: seq, Logs: entries}
}

func logWorld(t *testing.T, config ingest.Config) (*world, *fakeLogs) {
	t.Helper()
	w := newWorld(t, config)
	logs := &fakeLogs{}
	w.svc.SetLogWriter(logs)
	return w, logs
}

// Log batches (M3-07) are stored in the flush under the certificate's node and acknowledged in
// order with the metrics; a resend after a reconnect is acknowledged without storing it again.
func TestLogs_AreStoredAndAcknowledgedInOrder(t *testing.T) {
	w, logs := logWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	last := send(t, w, s, 1, t0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	now := w.clock.Now()
	if r := s.Accept(context.Background(), logBatch(last+1, logEntry(now, "upstream timed out"), logEntry(now, "recovered"))); r.Ack != 0 {
		t.Fatalf("acknowledged before the write: %+v", r)
	}
	if !s.Due() {
		t.Error("waiting logs make the session due, like events")
	}
	send(t, w, s, last+2, t0.Add(20*time.Second), 12)
	ack, err := s.Flush(context.Background())
	if err != nil || ack != last+1 {
		t.Fatalf("Flush() = %d, %v; want %d", ack, err, last+1)
	}
	if got := logs.messages(); len(got) != 2 || got[0] != "upstream timed out" || logs.nodes[0] != node || logs.orgs[0] != org {
		t.Errorf("written %v for %v/%v", got, logs.nodes, logs.orgs)
	}
	if n := w.svc.Stats().LogEntries.Load(); n != 2 {
		t.Errorf("stored entries = %d", n)
	}
	s.Close(context.Background())

	again := w.open(t)
	if r := again.Accept(context.Background(), logBatch(last+1, logEntry(now, "upstream timed out"))); r.Ack == 0 {
		t.Error("a resent batch is acknowledged at once")
	}
	if _, err := again.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(logs.messages()); got != 2 {
		t.Errorf("%d entries after the resend, want 2", got)
	}
}

// ADR 0031, Nachtrag M3-07: a Loki outage holds the acknowledgement back for at most
// LogRetryFor; then the logs are dropped and counted, the metrics go on, and later log batches
// are dropped at once until LogDownFor has passed.
func TestLogs_ALokiOutageHoldsMetricsBackOnlyBriefly(t *testing.T) {
	config := ingest.DefaultConfig()
	w, logs := logWorld(t, config)
	logs.setFail(fmt.Errorf("loki: %w", errors.New("connection refused")))
	s := w.open(t)
	last := send(t, w, s, 1, t0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	s.Accept(context.Background(), logBatch(last+1, logEntry(w.clock.Now(), "lost")))
	last = send(t, w, s, last+2, t0.Add(20*time.Second), 12)

	ack, err := s.Flush(context.Background())
	if err != nil || ack != last-2 {
		t.Fatalf("Flush() = %d, %v; want %d – only before the waiting logs", ack, err, last-2)
	}
	send(t, w, s, last+1, t0.Add(30*time.Second), 13) // sets the clock to the batch's time
	w.clock.Set(w.clock.Now().Add(config.LogRetryFor))
	ack, err = s.Flush(context.Background())
	if err != nil || ack != last {
		t.Fatalf("after the retry time: Flush() = %d, %v; want %d", ack, err, last)
	}
	stats := w.svc.Stats()
	if stats.LogsDroppedUnavailable.Load() != 1 || stats.LogEntries.Load() != 0 {
		t.Errorf("dropped %d, stored %d", stats.LogsDroppedUnavailable.Load(), stats.LogEntries.Load())
	}
	if !w.svc.LogsUnavailable() {
		t.Error("the outage is visible for the self-monitoring")
	}

	// While Loki counts as down, new log batches are dropped without a try.
	calls := logs.calls
	s.Accept(context.Background(), logBatch(last+2, logEntry(w.clock.Now(), "also lost")))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logs.calls != calls || stats.LogsDroppedUnavailable.Load() != 2 {
		t.Errorf("calls %d → %d, dropped %d", calls, logs.calls, stats.LogsDroppedUnavailable.Load())
	}
	// After LogDownFor it tries again.
	logs.setFail(nil)
	w.clock.Set(w.clock.Now().Add(config.LogDownFor + time.Second))
	s.Accept(context.Background(), logBatch(last+3, logEntry(w.clock.Now(), "back")))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := logs.messages(); len(got) != 1 || got[0] != "back" || w.svc.LogsUnavailable() {
		t.Errorf("written %v, unavailable %v", got, w.svc.LogsUnavailable())
	}
}

// Loki refuses for good (4xx): dropped at once, nothing held back.
func TestLogs_RejectedOnesAreDroppedAtOnce(t *testing.T) {
	w, logs := logWorld(t, ingest.DefaultConfig())
	logs.setFail(fmt.Errorf("%w: status 400", ingest.ErrLogsRejected))
	s := w.open(t)
	s.Accept(context.Background(), logBatch(1, logEntry(w.clock.Now(), "x")))
	if ack, err := s.Flush(context.Background()); err != nil || ack != 1 {
		t.Fatalf("Flush() = %d, %v; want 1", ack, err)
	}
	if w.svc.Stats().LogsDroppedRejected.Load() != 1 || w.svc.LogsUnavailable() {
		t.Error("counted as rejected, Loki is not down")
	}
}

// Time window: entries older than 7 days or 5 min ahead are dropped before they reach Loki.
func TestLogs_OutsideTheTimeWindowAreDropped(t *testing.T) {
	w, logs := logWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	now := w.clock.Now()
	s.Accept(context.Background(), logBatch(1,
		logEntry(now.Add(-8*24*time.Hour), "last week"), logEntry(now.Add(time.Hour), "future"), logEntry(now, "now")))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := logs.messages(); len(got) != 1 || got[0] != "now" || w.svc.Stats().LogsDroppedAge.Load() != 2 {
		t.Errorf("written %v, dropped %d", got, w.svc.Stats().LogsDroppedAge.Load())
	}
}

// A node's bytes per second are bounded across reconnects, and the organization's together.
func TestLogs_AFloodIsBoundedPerNodeAndOrganization(t *testing.T) {
	config := ingest.DefaultConfig()
	config.LogBytesRate, config.LogBytesBurst = 1000, 10_000
	config.OrgLogBytesRate, config.OrgLogBytesBurst = 1000, 15_000
	w, logs := logWorld(t, config)
	line := strings.Repeat("x", 1000)
	flood := func(s *ingest.Session, firstSeq uint64, n int) {
		for i := range n {
			s.Accept(context.Background(), logBatch(firstSeq+uint64(i), logEntry(w.clock.Now(), line)))
		}
		if _, err := s.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	s := w.open(t)
	flood(s, 1, 12)
	if got := len(logs.messages()); got != 10 {
		t.Errorf("%d entries passed, want the burst of 10", got)
	}
	s.Close(context.Background())
	flood(w.open(t), 100, 5)
	if got := len(logs.messages()); got != 10 {
		t.Errorf("a reconnect refilled the bucket: %d entries", got)
	}
	// Another node of the organization: its own bucket, but the organization's is nearly empty.
	other := identity.NodeID(uuid.New())
	w.store.AddNode(org, group, other)
	s2, err := w.svc.Open(context.Background(), org, other, ingest.HostFacts{Hostname: "web-02"}, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	flood(s2, 1, 10)
	if got := len(logs.messages()); got != 15 {
		t.Errorf("%d entries in all, want the organization's burst of 15", got)
	}
	if w.svc.Stats().LogsDroppedRate.Load() != 2+5+5 {
		t.Errorf("dropped for the rate = %d", w.svc.Stats().LogsDroppedRate.Load())
	}
}

// The unit comes from the host: beyond MaxUnitsPerNode distinct units a node's entries go to
// "other", so a host cannot grow Loki's streams without bound.
func TestLogs_TheUnitsOfANodeAreBounded(t *testing.T) {
	w, logs := logWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	var entries []ingest.LogEntry
	for i := range ingest.MaxUnitsPerNode + 5 {
		e := logEntry(w.clock.Now(), "x")
		e.Unit = fmt.Sprintf("unit-%d.service", i)
		entries = append(entries, e)
	}
	again := logEntry(w.clock.Now(), "known")
	again.Unit = "unit-0.service"
	entries = append(entries, again)
	s.Accept(context.Background(), logBatch(1, entries...))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	units := map[string]int{}
	for _, e := range logs.written {
		units[e.Unit]++
	}
	if len(units) != ingest.MaxUnitsPerNode+1 || units[ingest.OtherUnit] != 5 || units["unit-0.service"] != 2 {
		t.Errorf("%d units, %d other, unit-0 %d", len(units), units[ingest.OtherUnit], units["unit-0.service"])
	}
}

// Without a log store (no Loki configured) log batches are acknowledged and dropped.
func TestLogs_WithoutAStoreAreAcknowledgedAndDropped(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	s.Accept(context.Background(), logBatch(1, logEntry(w.clock.Now(), "x")))
	if ack, err := s.Flush(context.Background()); err != nil || ack != 1 {
		t.Fatalf("Flush() = %d, %v", ack, err)
	}
	if w.svc.Stats().LogsDroppedNoStore.Load() != 1 {
		t.Error("not counted")
	}
}
