package ingest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// updateStore stores like the database: the newer report of a node wins.
type updateStore struct {
	mu      sync.Mutex
	fail    bool
	reports map[identity.NodeID]update.Report
	writes  int
}

func (s *updateStore) Write(_ context.Context, _ tenant.OrgID, node identity.NodeID, r update.Report, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.fail {
		return false, errors.New("database down")
	}
	if s.reports == nil {
		s.reports = map[identity.NodeID]update.Report{}
	}
	if old, ok := s.reports[node]; ok && !r.CollectedAt.After(old.CollectedAt) {
		return false, nil
	}
	s.reports[node] = r
	return true, nil
}

func (s *updateStore) stored(n identity.NodeID) (update.Report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reports[n]
	return r, ok
}

func report(at time.Time, names ...string) *update.Report {
	r := update.Report{CollectedAt: at, Availability: update.Listed, Manager: update.Apt}
	for _, n := range names {
		r.Packages = append(r.Packages, update.Package{Name: n, Candidate: "2"})
	}
	return &r
}

func updateBatch(seq uint64, r *update.Report) ingest.Batch {
	return ingest.Batch{Seq: seq, Update: r}
}

func updateWorld(t *testing.T, config ingest.Config) (*world, *updateStore) {
	t.Helper()
	w := newWorld(t, config)
	store := &updateStore{}
	w.svc.SetUpdateWriter(store)
	return w, store
}

// A report is written at the next flush, normalized, and acknowledged in order behind the
// metric batches before it; it is no metric and changes no lag.
func TestUpdates_AreWrittenAtTheFlushAndAcknowledgedInOrder(t *testing.T) {
	w, store := updateWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	ctx := context.Background()
	last := send(t, w, s, 1, t0, 1, 2, 3)
	r := report(t0, "vim", "vim\u202e")
	if resp := s.Accept(ctx, updateBatch(last+1, r)); resp.Ack != 0 {
		t.Fatal("a report was acknowledged before it was stored")
	}
	if !s.Due() {
		t.Error("a pending report should flush at once")
	}
	if lag := w.svc.Stats().LagMillis.Load(); lag > 10_000 {
		t.Errorf("lag = %d ms: a report has no collection time of metrics", lag)
	}
	if ack, err := s.Flush(ctx); err != nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; the metric window before the report is still open", ack, err)
	}
	got, ok := store.stored(node)
	if !ok || len(got.Packages) != 2 || got.Packages[1].Name != "vim\uFFFD" || got.Total != 2 {
		t.Fatalf("stored = %+v, want the normalized report", got)
	}
	if s.Due() {
		t.Error("still due after the report was written")
	}
	send(t, w, s, last+2, t0.Add(10*time.Second), 4)
	if ack, err := s.Flush(ctx); err != nil || ack != last+1 {
		t.Errorf("Flush() = %d, %v; want the report's batch %d acknowledged", ack, err, last+1)
	}
	if n := w.svc.Stats().UpdateReports.Load(); n != 1 {
		t.Errorf("update reports = %d, want 1", n)
	}
}

// Several reports before a flush: only the newest is written. An older one, in the session or
// after the write (a resend of an earlier batch), is acknowledged and counted as stale.
func TestUpdates_TheNewestReportWins(t *testing.T) {
	w, store := updateWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	ctx := context.Background()
	w.clock.Set(t0.Add(time.Minute))
	s.Accept(ctx, updateBatch(1, report(t0.Add(-time.Minute), "old")))
	s.Accept(ctx, updateBatch(2, report(t0, "new")))
	s.Accept(ctx, updateBatch(3, report(t0.Add(-2*time.Minute), "older")))
	if ack, err := s.Flush(ctx); err != nil || ack != 3 {
		t.Fatalf("Flush() = %d, %v; want 3", ack, err)
	}
	if got, _ := store.stored(node); got.Packages[0].Name != "new" || store.writes != 1 {
		t.Errorf("stored %+v in %d writes, want the newest in one", got, store.writes)
	}
	s.Accept(ctx, updateBatch(4, report(t0.Add(-time.Minute), "resent")))
	if ack, err := s.Flush(ctx); err != nil || ack != 4 {
		t.Fatalf("Flush() = %d, %v; want 4", ack, err)
	}
	if got, _ := store.stored(node); got.Packages[0].Name != "new" {
		t.Errorf("an older report replaced the newer one: %+v", got)
	}
	if n := w.svc.Stats().UpdateReportsStale.Load(); n != 2 {
		t.Errorf("stale reports = %d, want 2", n)
	}
}

func TestUpdates_AWriteErrorAcknowledgesNothingAndRetries(t *testing.T) {
	w, store := updateWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	ctx := context.Background()
	w.clock.Set(t0.Add(time.Second))
	s.Accept(ctx, updateBatch(1, report(t0, "vim")))
	store.fail = true
	if ack, err := s.Flush(ctx); err == nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; want an error and no ack", ack, err)
	}
	store.fail = false
	if ack, err := s.Flush(ctx); err != nil || ack != 1 {
		t.Fatalf("retry Flush() = %d, %v; want 1", ack, err)
	}
	if _, ok := store.stored(node); !ok {
		t.Error("the retry stored nothing")
	}
}

// A clock far ahead would block every later report (the newer one wins); a very old one is of no
// use. Invalid reports are dropped too. All are acknowledged.
func TestUpdates_OutsideTheTimeWindowOrInvalidAreDroppedButAcknowledged(t *testing.T) {
	w, store := updateWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	ctx := context.Background()
	config := ingest.DefaultConfig()
	invalid := report(t0, "vim")
	invalid.Availability = update.AvailabilityUnknown
	for i, r := range []*update.Report{
		report(t0.Add(config.MaxFuture+time.Second), "future"),
		report(t0.Add(-config.UpdateMaxAge-time.Second), "ancient"),
		invalid,
	} {
		s.Accept(ctx, updateBatch(uint64(i+1), r))
	}
	if ack, err := s.Flush(ctx); err != nil || ack != 3 {
		t.Fatalf("Flush() = %d, %v; want 3", ack, err)
	}
	if store.writes != 0 {
		t.Errorf("%d writes, want none", store.writes)
	}
	if n := w.svc.Stats().UpdateReportsRejected.Load(); n != 3 {
		t.Errorf("rejected = %d, want 3", n)
	}
	// Just inside the window is fine.
	s.Accept(ctx, updateBatch(4, report(t0.Add(config.MaxFuture), "edge")))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.stored(node); !ok {
		t.Error("a report within MaxFuture was dropped")
	}
}

// A node gets a few reports an hour, across reconnects; the organization a budget of its own.
func TestUpdates_ReportsPerHourAreLimitedPerNodeAndOrganization(t *testing.T) {
	config := ingest.DefaultConfig()
	config.UpdateReportBurst, config.UpdateReportRate = 2, 2.0/3600
	config.OrgUpdateReportBurst, config.OrgUpdateReportRate = 3, 1.0/3600
	w, store := updateWorld(t, config)
	ctx := context.Background()
	seq := uint64(0)
	deliver := func(s *ingest.Session, at time.Time) {
		t.Helper()
		seq++
		w.clock.Set(at)
		s.Accept(ctx, updateBatch(seq, report(at, "vim")))
		if ack, err := s.Flush(ctx); err != nil || ack != seq {
			t.Fatalf("Flush() = %d, %v; want %d (dropped reports are acknowledged)", ack, err, seq)
		}
	}
	deliver(w.open(t), t0)
	deliver(w.open(t), t0.Add(time.Second)) // a reconnect gains nothing
	deliver(w.open(t), t0.Add(2*time.Second))
	if store.writes != 2 || w.svc.Stats().UpdateReportsDropped.Load() != 1 {
		t.Fatalf("writes = %d, dropped = %d; want 2, 1", store.writes, w.svc.Stats().UpdateReportsDropped.Load())
	}
	// Half an hour refills one report of the node.
	deliver(w.open(t), t0.Add(31*time.Minute))
	if store.writes != 3 {
		t.Fatalf("writes = %d after the refill, want 3", store.writes)
	}

	// Another node of the organization: the organization's budget (3) is spent.
	other := identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000f2"))
	w.store.AddNode(org, group, other)
	s, err := w.svc.Open(ctx, org, other, ingest.HostFacts{Hostname: "web-02"}, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	seq = 0
	deliver(s, t0.Add(32*time.Minute))
	if _, ok := store.stored(other); ok {
		t.Error("the organization's budget did not hold")
	}
}

func TestUpdates_WithoutAWriterAreDroppedButAcknowledged(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	w.clock.Set(t0.Add(time.Second))
	s.Accept(context.Background(), updateBatch(1, report(t0, "vim")))
	if ack, err := s.Flush(context.Background()); err != nil || ack != 1 {
		t.Fatalf("Flush() = %d, %v; want 1", ack, err)
	}
	if n := w.svc.Stats().UpdateReportsRejected.Load(); n != 1 {
		t.Errorf("rejected = %d, want 1", n)
	}
}
