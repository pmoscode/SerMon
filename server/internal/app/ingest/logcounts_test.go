package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// countKey is a bucket of the fake counter.
type countKey struct {
	minute   time.Time
	source   ingest.LogSource
	severity uint8
}

// fakeCounter behaves like the store (log_counts_add): the counts of a batch at or below the
// highest counted sequence number of the node are skipped.
type fakeCounter struct {
	mu     sync.Mutex
	counts map[countKey]uint64
	marks  map[identity.NodeID]uint64
	orgs   []tenant.OrgID
	calls  int
	fail   error
}

func newFakeCounter() *fakeCounter {
	return &fakeCounter{counts: map[countKey]uint64{}, marks: map[identity.NodeID]uint64{}}
}

func (f *fakeCounter) CountLogs(_ context.Context, org tenant.OrgID, node identity.NodeID, counts []ingest.LogCount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	mark := f.marks[node]
	for _, c := range counts {
		if c.Seq <= mark {
			continue
		}
		f.counts[countKey{c.Minute, c.Source, c.Severity}] += c.Lines
		f.marks[node] = max(f.marks[node], c.Seq)
	}
	f.orgs = append(f.orgs, org)
	return nil
}

func (f *fakeCounter) total() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n uint64
	for _, v := range f.counts {
		n += v
	}
	return n
}

func (f *fakeCounter) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func countWorld(t *testing.T, config ingest.Config) (*world, *fakeLogs, *fakeCounter) {
	t.Helper()
	w, logs := logWorld(t, config)
	counter := newFakeCounter()
	w.svc.SetLogCounter(counter)
	return w, logs, counter
}

// V1-24: what Loki stored is counted per minute, source and severity. A line at a whole minute
// belongs to that minute, a line just after it to the next (Loki counts (t − step, t]); the
// severities beyond debug are unknown, as the writer labels them.
func TestLogCounts_CountWhatLokiStoredPerMinuteSourceAndSeverity(t *testing.T) {
	w, _, counter := countWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	minute := w.clock.Now().Truncate(time.Minute).Add(-5 * time.Minute)
	entry := func(at time.Time, source ingest.LogSource, severity uint8) ingest.LogEntry {
		return ingest.LogEntry{At: at, Source: source, Unit: "nginx.service", Severity: severity, Message: "x"}
	}
	s.Accept(context.Background(), logBatch(1,
		entry(minute, ingest.LogSourceJournal, 4),
		entry(minute.Add(-30*time.Second), ingest.LogSourceJournal, 4),
		entry(minute.Add(time.Nanosecond), ingest.LogSourceJournal, 4),
		entry(minute.Add(time.Second), ingest.LogSourceContainer, 7),
	))
	s.Accept(context.Background(), logBatch(2,
		entry(minute.Add(2*time.Second), ingest.LogSourceContainer, 7),
		entry(minute.Add(3*time.Second), ingest.LogSourceFile, 42),
	))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := minute.Add(time.Minute)
	want := map[countKey]uint64{
		{minute, ingest.LogSourceJournal, 4}: 2,
		{next, ingest.LogSourceJournal, 4}:   1,
		{next, ingest.LogSourceContainer, 7}: 2,
		{next, ingest.LogSourceFile, 0}:      1,
	}
	if len(counter.counts) != len(want) {
		t.Errorf("counts %v, want %v", counter.counts, want)
	}
	for k, v := range want {
		if counter.counts[k] != v {
			t.Errorf("%v = %d, want %d (all: %v)", k, counter.counts[k], v, counter.counts)
		}
	}
	if len(counter.orgs) != 1 || counter.orgs[0] != org {
		t.Errorf("counted in %v, want %v", counter.orgs, org)
	}
}

// Only what Loki took is counted: rejected or dropped lines are not, lines after a retry once.
func TestLogCounts_OnlyWhatLokiStoredIsCounted(t *testing.T) {
	config := ingest.DefaultConfig()
	w, logs, counter := countWorld(t, config)
	s := w.open(t)
	logs.setFail(fmt.Errorf("%w: status 400", ingest.ErrLogsRejected))
	s.Accept(context.Background(), logBatch(1, logEntry(w.clock.Now(), "rejected")))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs.setFail(errors.New("connection refused"))
	s.Accept(context.Background(), logBatch(2, logEntry(w.clock.Now(), "retried")))
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := counter.total(); n != 0 {
		t.Fatalf("%d lines counted that Loki did not store", n)
	}
	logs.setFail(nil)
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := counter.total(); n != 1 {
		t.Errorf("%d lines counted after the retry, want 1", n)
	}
}

// A failing count store holds the acknowledgement back – the agent would send the batch again
// and Loki keeps it once – without writing the lines to Loki again; after LogRetryFor the counts
// are given up (counted in the stats) and the acknowledgement goes on.
func TestLogCounts_AFailingCountStoreHoldsTheAcknowledgementBriefly(t *testing.T) {
	config := ingest.DefaultConfig()
	w, logs, counter := countWorld(t, config)
	s := w.open(t)
	counter.setFail(errors.New("database down"))
	s.Accept(context.Background(), logBatch(1, logEntry(w.clock.Now(), "a")))
	if ack, err := s.Flush(context.Background()); err != nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; want held back", ack, err)
	}
	s.Accept(context.Background(), logBatch(2, logEntry(w.clock.Now(), "b")))
	if ack, err := s.Flush(context.Background()); err != nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; want held back", ack, err)
	}
	if logs.calls != 2 || len(logs.messages()) != 2 {
		t.Errorf("Loki written %d times with %v; each batch once", logs.calls, logs.messages())
	}
	counter.setFail(nil)
	if ack, err := s.Flush(context.Background()); err != nil || ack != 2 {
		t.Fatalf("after recovery: Flush() = %d, %v; want 2", ack, err)
	}
	if n := counter.total(); n != 2 || logs.calls != 2 {
		t.Errorf("counted %d, Loki calls %d", n, logs.calls)
	}

	counter.setFail(errors.New("database down"))
	s.Accept(context.Background(), logBatch(3, logEntry(w.clock.Now(), "c")))
	if ack, _ := s.Flush(context.Background()); ack != 0 {
		t.Fatalf("ack %d while the counts fail", ack)
	}
	w.clock.Set(w.clock.Now().Add(config.LogRetryFor))
	if ack, err := s.Flush(context.Background()); err != nil || ack != 3 {
		t.Fatalf("after LogRetryFor: Flush() = %d, %v; want 3", ack, err)
	}
	if n := w.svc.Stats().LogCountsDropped.Load(); n != 1 {
		t.Errorf("dropped counts of %d lines, want 1", n)
	}
	counter.setFail(nil)
	if _, err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := counter.total(); n != 2 {
		t.Errorf("the given-up counts came back: %d", n)
	}
}

// The acknowledgement stops before the first batch that still waits, whether for Loki or for its
// counts.
func TestLogCounts_TheAcknowledgementStopsAtTheFirstWaitingBatch(t *testing.T) {
	w, logs, counter := countWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	counter.setFail(errors.New("database down"))
	s.Accept(context.Background(), logBatch(1, logEntry(w.clock.Now(), "counted later")))
	if ack, _ := s.Flush(context.Background()); ack != 0 {
		t.Fatalf("ack %d while the counts of 1 wait", ack)
	}
	logs.setFail(errors.New("connection refused"))
	s.Accept(context.Background(), logBatch(2, logEntry(w.clock.Now(), "for Loki later")))
	s.Accept(context.Background(), ingest.Batch{Seq: 3, Unhandled: true})
	if ack, _ := s.Flush(context.Background()); ack != 0 {
		t.Errorf("ack %d with the counts of 1 and the lines of 2 waiting, want none", ack)
	}
	counter.setFail(nil)
	if ack, _ := s.Flush(context.Background()); ack != 1 {
		t.Errorf("ack %d once 1 is counted, want 1 (2 waits for Loki)", ack)
	}
}

// The batch Loki stored is resent when its acknowledgement was lost (the metric write failed):
// Loki keeps it once, the counts skip it (the store's mark).
func TestLogCounts_AResendAfterALostAcknowledgementIsCountedOnce(t *testing.T) {
	w, logs, counter := countWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	w.store.fail = true
	s.Accept(context.Background(), logBatch(1, logEntry(w.clock.Now(), "once")))
	if _, err := s.Flush(context.Background()); err == nil {
		t.Fatal("the metric write fails")
	}
	s.Close(context.Background())
	w.store.fail = false

	again := w.open(t)
	again.Accept(context.Background(), logBatch(1, logEntry(w.clock.Now(), "once")))
	if ack, err := again.Flush(context.Background()); err != nil || ack != 1 {
		t.Fatalf("Flush() = %d, %v", ack, err)
	}
	if len(logs.messages()) != 2 || counter.calls != 2 || counter.total() != 1 {
		t.Errorf("Loki got %d, counter called %d times, counted %d", len(logs.messages()), counter.calls, counter.total())
	}
}
