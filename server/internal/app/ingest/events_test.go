package ingest_test

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// eventStore stores like the database: per node only batches above the stored sequence.
type eventStore struct {
	mu     sync.Mutex
	fail   bool
	seq    map[identity.NodeID]uint64
	events []secevent.Event
	writes int
}

func (s *eventStore) Write(_ context.Context, _ tenant.OrgID, node identity.NodeID, batches []secevents.Batch) ([]secevents.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.fail {
		return nil, errors.New("database down")
	}
	if s.seq == nil {
		s.seq = map[identity.NodeID]uint64{}
	}
	var written []secevents.Batch
	for _, b := range batches {
		if b.Seq <= s.seq[node] {
			continue
		}
		s.seq[node] = b.Seq
		s.events = append(s.events, b.Events...)
		written = append(written, b)
	}
	return written, nil
}

func (s *eventStore) stored() []secevent.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]secevent.Event(nil), s.events...)
}

func attempt(at time.Time, user string) secevent.Event {
	return secevent.Event{
		OccurredAt: at, Kind: secevent.SSHLoginFailed, User: user,
		Remote: &secevent.Remote{IP: netip.MustParseAddr("192.0.2.7"), Port: 22},
	}
}

func eventBatch(seq uint64, events ...secevent.Event) ingest.Batch {
	return ingest.Batch{Seq: seq, Events: events}
}

func eventWorld(t *testing.T, config ingest.Config) (*world, *eventStore) {
	t.Helper()
	w := newWorld(t, config)
	events := &eventStore{}
	w.svc.SetEventWriter(events)
	return w, events
}

// Event and metric batches share the agent's sequence numbers: an event batch is written at
// the next flush and acknowledged in order behind the metric batches before it.
func TestEvents_AreWrittenAtTheFlushAndAcknowledgedInOrder(t *testing.T) {
	w, events := eventWorld(t, ingest.DefaultConfig())
	s := w.open(t)
	ctx := context.Background()
	last := send(t, w, s, 1, t0, 1, 2, 3) // window 12:00:00, still open
	if r := s.Accept(ctx, eventBatch(last+1, attempt(t0, "root"))); r.Ack != 0 {
		t.Fatal("an event batch was acknowledged before it was stored")
	}
	if !s.Due() {
		t.Error("a pending event batch should flush at once")
	}
	if lag := w.svc.Stats().LagMillis.Load(); lag > 10_000 {
		t.Errorf("lag = %d ms: an event batch has no collection time", lag)
	}
	ack, err := s.Flush(ctx)
	if err != nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; the metric window before the events is still open", ack, err)
	}
	got := events.stored()
	if len(got) != 1 || got[0].Node != node || got[0].User != "root" {
		t.Fatalf("stored = %+v, want the event with the session's node", got)
	}
	send(t, w, s, last+2, t0.Add(10*time.Second), 4) // closes the window
	if ack, err := s.Flush(ctx); err != nil || ack != last+1 {
		t.Errorf("Flush() = %d, %v; want the event batch %d acknowledged", ack, err, last+1)
	}
}

// A batch stored before the acknowledgement got lost comes again after a reconnect.
func TestEvents_ResentAfterAReconnectAreStoredOnce(t *testing.T) {
	w, events := eventWorld(t, ingest.DefaultConfig())
	ctx := context.Background()
	first := w.open(t)
	w.clock.Set(t0.Add(time.Second))
	first.Accept(ctx, eventBatch(7, attempt(t0, "a"), attempt(t0, "b")))
	w.store.fail = true // the acknowledgement (metric store) fails after the events were stored
	if _, err := first.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded although the sequence could not be stored")
	}
	w.store.fail = false
	if got := events.stored(); len(got) != 2 {
		t.Fatalf("%d events stored before the failed acknowledgement, want 2", len(got))
	}

	second := w.open(t)
	second.Accept(ctx, eventBatch(7, attempt(t0, "a"), attempt(t0, "b")))
	second.Accept(ctx, eventBatch(8, attempt(t0, "c")))
	ack, err := second.Flush(ctx)
	if err != nil || ack != 8 {
		t.Fatalf("Flush() = %d, %v; want 8", ack, err)
	}
	if got := events.stored(); len(got) != 3 {
		t.Errorf("%d events stored, want a, b, c once", len(got))
	}
	if d := w.svc.Stats().EventDuplicates.Load(); d != 1 {
		t.Errorf("event duplicates = %d, want 1", d)
	}
}

func TestEvents_AWriteErrorAcknowledgesNothingAndRetries(t *testing.T) {
	w, events := eventWorld(t, ingest.DefaultConfig())
	ctx := context.Background()
	s := w.open(t)
	w.clock.Set(t0.Add(time.Second))
	events.fail = true
	s.Accept(ctx, eventBatch(1, attempt(t0, "root")))
	if ack, err := s.Flush(ctx); err == nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; want an error and no ack", ack, err)
	}
	events.fail = false
	if ack, err := s.Flush(ctx); err != nil || ack != 1 || len(events.stored()) != 1 {
		t.Errorf("retry: Flush() = %d, %v, %d events", ack, err, len(events.stored()))
	}
}

// The partitions hold a week back and five minutes ahead; a wrong agent clock or a forged time
// must not fail the batch or end up in a month without a partition.
func TestEvents_OutsideTheTimeWindowAreDroppedButAcknowledged(t *testing.T) {
	w, events := eventWorld(t, ingest.DefaultConfig())
	ctx := context.Background()
	s := w.open(t)
	now := t0.Add(time.Second)
	w.clock.Set(now)
	s.Accept(ctx, eventBatch(1, attempt(now.Add(-8*24*time.Hour), "ancient"), attempt(now.Add(10*time.Minute), "future"), attempt(now.Add(-6*24*time.Hour), "kept")))
	if ack, err := s.Flush(ctx); err != nil || ack != 1 {
		t.Fatalf("Flush() = %d, %v", ack, err)
	}
	if got := events.stored(); len(got) != 1 || got[0].User != "kept" {
		t.Errorf("stored = %+v", got)
	}
	if r := w.svc.Stats().EventsRejected.Load(); r != 2 {
		t.Errorf("rejected = %d, want 2", r)
	}
}

func TestEvents_InvalidEventsAreDropped(t *testing.T) {
	w, events := eventWorld(t, ingest.DefaultConfig())
	ctx := context.Background()
	s := w.open(t)
	w.clock.Set(t0.Add(time.Second))
	bad := attempt(t0, "x")
	bad.Kind = "ssh.root"
	s.Accept(ctx, eventBatch(1, bad, attempt(t0, "ok")))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := events.stored(); len(got) != 1 || got[0].User != "ok" {
		t.Errorf("stored = %+v", got)
	}
}

// A flood of one node is cut at the bucket; the bucket belongs to the node, so a reconnect
// starts empty instead of full.
func TestEvents_AFloodIsLimitedPerNodeAcrossReconnects(t *testing.T) {
	config := ingest.DefaultConfig()
	config.EventRate, config.EventBurst = 10, 100
	w, events := eventWorld(t, config)
	ctx := context.Background()
	flood := func(s *ingest.Session, seq uint64, n int) {
		list := make([]secevent.Event, n)
		for i := range list {
			list[i] = attempt(t0, "u")
		}
		s.Accept(ctx, eventBatch(seq, list...))
		if _, err := s.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	w.clock.Set(t0.Add(time.Second))
	flood(w.open(t), 1, 500)
	if got := len(events.stored()); got != 100 {
		t.Fatalf("%d events stored from 500, want the burst of 100", got)
	}
	flood(w.open(t), 2, 500) // reconnect, same second
	if got := len(events.stored()); got != 100 {
		t.Errorf("%d events after a reconnect, want still 100", got)
	}
	w.clock.Set(t0.Add(11 * time.Second)) // 10 s at 10 per second
	flood(w.open(t), 3, 500)
	if got := len(events.stored()); got != 200 {
		t.Errorf("%d events after 10 s, want 200", got)
	}
	if d := w.svc.Stats().EventsDropped.Load(); d != 1300 {
		t.Errorf("dropped = %d, want 1300", d)
	}
}

// Without an event store (no database) event batches are acknowledged and dropped.
func TestEvents_WithoutAWriterAreDropped(t *testing.T) {
	w := newWorld(t, ingest.DefaultConfig())
	ctx := context.Background()
	s := w.open(t)
	w.clock.Set(t0.Add(time.Second))
	s.Accept(ctx, eventBatch(1, attempt(t0, "root")))
	if ack, err := s.Flush(ctx); err != nil || ack != 1 {
		t.Errorf("Flush() = %d, %v", ack, err)
	}
}

// IPv4-mapped addresses are stored as IPv4, so that a filter for 192.0.2.0/24 finds them.
func TestEvents_MappedAddressesAreStoredAsIPv4(t *testing.T) {
	w, events := eventWorld(t, ingest.DefaultConfig())
	ctx := context.Background()
	s := w.open(t)
	w.clock.Set(t0.Add(time.Second))
	mapped := attempt(t0, "root")
	mapped.Remote = &secevent.Remote{IP: netip.MustParseAddr("::ffff:192.0.2.7"), Port: 22}
	s.Accept(ctx, eventBatch(1, mapped))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := events.stored(); len(got) != 1 || got[0].Remote.IP != netip.MustParseAddr("192.0.2.7") {
		t.Errorf("stored = %+v", got)
	}
}
