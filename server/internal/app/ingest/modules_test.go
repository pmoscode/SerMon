package ingest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// moduleStore keeps the newest reports like the database.
type moduleStore struct {
	mu     sync.Mutex
	fail   bool
	health map[identity.NodeID]module.HealthReport
	hints  map[identity.NodeID]module.Hints
	writes int
}

func (s *moduleStore) WriteModuleHealth(_ context.Context, _ tenant.OrgID, n identity.NodeID, r module.HealthReport, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.fail {
		return errors.New("database down")
	}
	s.health[n] = r
	return nil
}

func (s *moduleStore) WriteModuleHints(_ context.Context, _ tenant.OrgID, n identity.NodeID, h module.Hints, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.fail {
		return errors.New("database down")
	}
	s.hints[n] = h
	return nil
}

func moduleWorld(t *testing.T) (*world, *moduleStore) {
	t.Helper()
	w := newWorld(t, ingest.DefaultConfig())
	catalog, err := module.NewCatalog(map[string]string{"nginx": `{"type":"object","additionalProperties":false,"properties":{"port":{"type":"integer","minimum":1,"maximum":65535}}}`})
	if err != nil {
		t.Fatal(err)
	}
	store := &moduleStore{health: map[identity.NodeID]module.HealthReport{}, hints: map[identity.NodeID]module.Hints{}}
	w.svc.SetModuleWriter(store, catalog)
	return w, store
}

// The newest health report and hints of a session are written at the flush (not at once: an
// agent can't make the server write on every batch), normalized, and acknowledged in order.
func TestModules_ReportsAreWrittenAtTheFlushAndAcknowledgedInOrder(t *testing.T) {
	w, store := moduleWorld(t)
	s := w.open(t)
	ctx := context.Background()
	last := send(t, w, s, 1, t0, 1, 2, 3)
	old := &module.HealthReport{CollectedAt: t0, Modules: []module.Health{{Module: "nginx", State: module.StateError, Reason: "old"}}}
	newest := &module.HealthReport{CollectedAt: t0.Add(time.Second), Modules: []module.Health{
		{Module: "nginx", State: module.StateOK, Reason: "fine\u202e"}, {Module: "redis", State: module.StateOK},
	}}
	hints := &module.Hints{CollectedAt: t0, Hints: []module.Hint{{Module: "nginx", Reasons: []string{"port:80"}, Suggested: `{"port":80}`}}}
	for i, b := range []ingest.Batch{{Seq: last + 1, ModuleHealth: old}, {Seq: last + 2, ModuleHealth: newest}, {Seq: last + 3, ModuleHints: hints}} {
		if resp := s.Accept(ctx, b); resp.Ack != 0 {
			t.Fatalf("batch %d acknowledged before it was stored", i)
		}
	}
	if s.Due() {
		t.Error("module reports must not force an early flush")
	}
	if ack, err := s.Flush(ctx); err != nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; the metric window before is still open", ack, err)
	}
	got := store.health[node]
	if store.writes != 2 || len(got.Modules) != 1 || got.Modules[0].State != module.StateOK || got.Modules[0].Reason != "fine\uFFFD" {
		t.Fatalf("stored %+v after %d writes: want only the newest, normalized", got, store.writes)
	}
	if h := store.hints[node]; len(h.Hints) != 1 || h.Hints[0].Suggested != `{"port":80}` {
		t.Errorf("hints %+v", h)
	}
	send(t, w, s, last+4, t0.Add(20*time.Second), 4)
	if ack, err := s.Flush(ctx); err != nil || ack != last+3 {
		t.Fatalf("Flush() = %d, %v; want %d", ack, err, last+3)
	}
	if store.writes != 2 {
		t.Errorf("%d writes: written reports are not written again", store.writes)
	}
}

func TestModules_AFailedWriteAcknowledgesNothingAndRetries(t *testing.T) {
	w, store := moduleWorld(t)
	s := w.open(t)
	ctx := context.Background()
	store.fail = true
	r := &module.HealthReport{CollectedAt: t0, Modules: []module.Health{{Module: "nginx", State: module.StateOK}}}
	s.Accept(ctx, ingest.Batch{Seq: 1, ModuleHealth: r})
	if ack, err := s.Flush(ctx); err == nil || ack != 0 {
		t.Fatalf("Flush() = %d, %v; want an error", ack, err)
	}
	store.fail = false
	if ack, err := s.Flush(ctx); err != nil || ack != 1 || len(store.health[node].Modules) != 1 {
		t.Fatalf("retry Flush() = %d, %v, %+v", ack, err, store.health)
	}
}

// Reports from a wrong clock, and all of them without a writer, are acknowledged and dropped.
func TestModules_ReportsOutsideTheTimeWindowAreDropped(t *testing.T) {
	w, store := moduleWorld(t)
	s := w.open(t)
	ctx := context.Background()
	for i, at := range []time.Time{t0.Add(10 * time.Minute), t0.Add(-25 * time.Hour)} {
		s.Accept(ctx, ingest.Batch{Seq: uint64(2*i + 1), ModuleHealth: &module.HealthReport{CollectedAt: at}})
		s.Accept(ctx, ingest.Batch{Seq: uint64(2*i + 2), ModuleHints: &module.Hints{CollectedAt: at}})
	}
	if ack, err := s.Flush(ctx); err != nil || ack != 4 || store.writes != 0 {
		t.Fatalf("Flush() = %d, %v after %d writes", ack, err, store.writes)
	}
	if n := w.svc.Stats().ModuleReportsRejected.Load(); n != 4 {
		t.Errorf("rejected %d", n)
	}

	plain := newWorld(t, ingest.DefaultConfig())
	ps := plain.open(t)
	ps.Accept(ctx, ingest.Batch{Seq: 1, ModuleHealth: &module.HealthReport{CollectedAt: t0}})
	if ack, err := ps.Flush(ctx); err != nil || ack != 1 {
		t.Fatalf("without a writer: %d, %v", ack, err)
	}
}
