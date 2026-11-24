package liveview_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	other = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	node  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	t0    = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
)

// agents records the commands to the agent sessions of this instance.
type agents struct {
	mu        sync.Mutex
	connected bool
	starts    []bool // hide_cmdline of each start
	stops     int
}

func (a *agents) StartLiveView(o tenant.OrgID, n identity.NodeID, interval time.Duration, hide bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.connected || o != org || n != node || interval != liveview.Interval {
		return false
	}
	a.starts = append(a.starts, hide)
	return true
}

func (a *agents) StopLiveView(tenant.OrgID, identity.NodeID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stops++
}

func (a *agents) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.starts), a.stops
}

func secret() *string { s := "mysql --password=***"; return &s }

func snapshot() liveview.Snapshot {
	return liveview.Snapshot{TakenAt: t0, Processes: []liveview.Process{{PID: 7, Name: "mysql", State: "S", Cmdline: secret()}}}
}

func next(t *testing.T, s *liveview.Subscription) liveview.Frame {
	t.Helper()
	select {
	case f := <-s.Frames():
		return f
	case <-time.After(time.Second):
		t.Fatal("no frame")
		return liveview.Frame{}
	}
}

func none(t *testing.T, s *liveview.Subscription) {
	t.Helper()
	select {
	case f := <-s.Frames():
		t.Fatalf("unexpected frame %+v", f)
	default:
	}
}

func TestHub_ViewersShareOneAgentStream(t *testing.T) {
	a := &agents{connected: true}
	hub := liveview.NewHub(a)
	first := hub.Subscribe(org, node, false, t0)
	second := hub.Subscribe(org, node, false, t0.Add(time.Second))
	if starts, _ := a.counts(); starts != 1 {
		t.Fatalf("%d starts for two viewers, want 1", starts)
	}
	hub.Deliver(org, node, snapshot())
	for _, s := range []*liveview.Subscription{first, second} {
		if f := next(t, s); f.Snapshot == nil || *f.Snapshot.Processes[0].Cmdline != "mysql --password=***" {
			t.Errorf("frame = %+v", f)
		}
	}
	// Another organization's snapshot for the node reaches nobody.
	hub.Deliver(other, node, snapshot())
	none(t, first)

	// The lease is renewed while someone watches.
	hub.Maintain(t0.Add(liveview.RenewEvery - time.Second))
	if starts, _ := a.counts(); starts != 1 {
		t.Errorf("renewed too early")
	}
	hub.Maintain(t0.Add(liveview.RenewEvery))
	if starts, _ := a.counts(); starts != 2 {
		t.Errorf("not renewed after %v", liveview.RenewEvery)
	}

	// Stop 30 s after the last viewer – unless one comes back.
	first.Close(t0.Add(30 * time.Second))
	second.Close(t0.Add(31 * time.Second))
	hub.Maintain(t0.Add(31*time.Second + liveview.StopAfter - time.Second))
	if _, stops := a.counts(); stops != 0 {
		t.Errorf("stopped too early")
	}
	again := hub.Subscribe(org, node, false, t0.Add(50*time.Second))
	hub.Maintain(t0.Add(31*time.Second + liveview.StopAfter))
	// No stop; the lease was due and renewed.
	if starts, stops := a.counts(); stops != 0 || starts != 3 {
		t.Errorf("a viewer came back: %d starts, %d stops", starts, stops)
	}
	again.Close(t0.Add(60 * time.Second))
	hub.Maintain(t0.Add(60*time.Second + liveview.StopAfter))
	if _, stops := a.counts(); stops != 1 {
		t.Errorf("%d stops after the last viewer, want 1", stops)
	}
	// Closing twice is harmless.
	again.Close(t0.Add(time.Hour))
}

func TestHub_HidesCommandLinesItself(t *testing.T) {
	a := &agents{connected: true}
	hub := liveview.NewHub(a)
	s := hub.Subscribe(org, node, true, t0)
	if a.starts[0] != true {
		t.Error("the agent was not asked to hide")
	}
	// An agent that sends them anyway (old version, bug): the server removes them.
	hub.Deliver(org, node, snapshot())
	p := next(t, s).Snapshot.Processes[0]
	if p.Cmdline != nil || !p.CmdlineHidden {
		t.Errorf("process = %+v", p)
	}
	// A viewer with the switch changed: the agent gets the new setting.
	hub.Subscribe(org, node, false, t0.Add(time.Second))
	if starts, _ := a.counts(); starts != 2 || a.starts[1] {
		t.Errorf("starts = %v", a.starts)
	}
}

func TestHub_SaysOfflineAndRefusedAndRestartsOnReconnect(t *testing.T) {
	a := &agents{}
	hub := liveview.NewHub(a)
	s := hub.Subscribe(org, node, false, t0)
	if f := next(t, s); !f.Offline {
		t.Fatalf("not connected: %+v", f)
	}
	a.mu.Lock()
	a.connected = true
	a.mu.Unlock()
	hub.Connected(org, node)
	if starts, _ := a.counts(); starts != 1 {
		t.Fatal("not started after the agent connected")
	}
	hub.Disconnected(org, node)
	if f := next(t, s); !f.Offline {
		t.Errorf("after the disconnect: %+v", f)
	}
	hub.Connected(org, node)
	hub.Refused(org, node, "agent.toml forbids the live view")
	if f := next(t, s); f.Refused != "agent.toml forbids the live view" {
		t.Errorf("refusal: %+v", f)
	}
	// A refused feed is not renewed.
	hub.Maintain(t0.Add(time.Hour))
	if starts, _ := a.counts(); starts != 2 {
		t.Errorf("%d starts after the refusal", starts)
	}
	// Without viewers a connect starts nothing, also in the grace before the stop.
	s.Close(t0.Add(time.Hour))
	hub.Connected(org, node)
	if starts, _ := a.counts(); starts != 2 {
		t.Errorf("started without viewers")
	}
}

// A slow viewer gets the newest snapshot, not a growing queue; the others are not held up.
func TestHub_ASlowViewerGetsTheNewest(t *testing.T) {
	a := &agents{connected: true}
	clock := t0
	hub := liveview.NewHub(a, liveview.WithHubClock(func() time.Time { return clock }))
	slow := hub.Subscribe(org, node, false, t0)
	for i := range 5 {
		s := snapshot()
		s.Omitted = uint32(i)
		hub.Deliver(org, node, s)
		clock = clock.Add(liveview.Interval)
	}
	if f := next(t, slow); f.Snapshot.Omitted != 4 {
		t.Errorf("got snapshot %d, want the newest", f.Snapshot.Omitted)
	}
	none(t, slow)
}

// M3-29: every viewer has its own switch. The agent sends command lines while one viewer may see
// them; the others get them removed, also when the switch changes between two viewers.
func TestHub_HidesPerViewer(t *testing.T) {
	a := &agents{connected: true}
	clock := t0
	hub := liveview.NewHub(a, liveview.WithHubClock(func() time.Time { return clock }))
	hidden := hub.Subscribe(org, node, true, t0)
	shown := hub.Subscribe(org, node, false, t0)
	if a.starts[len(a.starts)-1] {
		t.Fatalf("starts = %v: one viewer may see command lines", a.starts)
	}
	hub.Deliver(org, node, snapshot())
	if p := next(t, hidden).Snapshot.Processes[0]; p.Cmdline != nil || !p.CmdlineHidden {
		t.Errorf("the hiding viewer got %+v", p)
	}
	if f := next(t, shown); f.Snapshot.Processes[0].Cmdline == nil || f.Snapshot.CmdlinesHidden {
		t.Errorf("the other viewer got %+v", f.Snapshot)
	}
	// A late viewer with the old switch doesn't turn it off for the others.
	late := hub.Subscribe(org, node, false, t0)
	shown.Close(t0)
	late.Close(t0)
	// Only hiding viewers left: the agent is asked to hide again.
	if !a.starts[len(a.starts)-1] {
		t.Errorf("starts = %v: only hiding viewers are left", a.starts)
	}
	clock = clock.Add(liveview.Interval)
	hub.Deliver(org, node, snapshot())
	if f := next(t, hidden); !f.Snapshot.CmdlinesHidden {
		t.Errorf("after the others left: %+v", f.Snapshot)
	}
}

// M3-29: an agent sends at most one snapshot per half interval; it can't flood the viewers.
func TestHub_DropsSnapshotsFasterThanTheInterval(t *testing.T) {
	a := &agents{connected: true}
	clock := t0
	hub := liveview.NewHub(a, liveview.WithHubClock(func() time.Time { return clock }))
	if hub.Wants(org, node) {
		t.Error("wants snapshots without viewers")
	}
	s := hub.Subscribe(org, node, false, t0)
	if !hub.Wants(org, node) || hub.Wants(other, node) {
		t.Error("Wants() with a viewer")
	}
	hub.Deliver(org, node, snapshot())
	next(t, s)
	clock = clock.Add(liveview.Interval/2 - time.Millisecond)
	hub.Deliver(org, node, snapshot())
	none(t, s)
	clock = clock.Add(time.Millisecond)
	hub.Deliver(org, node, snapshot())
	next(t, s)
}
