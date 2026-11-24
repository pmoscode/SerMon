package agentapi_test

import (
	"crypto/tls"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
)

// liveSink records what the sessions pass to the live view hub.
type liveSink struct {
	mu           sync.Mutex
	connected    []identity.NodeID
	disconnected []identity.NodeID
	snapshots    []liveview.Snapshot
	refusals     []string
	// skip answers Wants with false so many times (M3-29).
	skip, asked int
}

func (l *liveSink) Wants(tenant.OrgID, identity.NodeID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked++
	if l.skip > 0 {
		l.skip--
		return false
	}
	return true
}

func (l *liveSink) Connected(_ tenant.OrgID, n identity.NodeID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.connected = append(l.connected, n)
}

func (l *liveSink) Disconnected(_ tenant.OrgID, n identity.NodeID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.disconnected = append(l.disconnected, n)
}

func (l *liveSink) Deliver(o tenant.OrgID, n identity.NodeID, s liveview.Snapshot) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if o == orgX && n == nodeX {
		l.snapshots = append(l.snapshots, s)
	}
}

func (l *liveSink) Refused(o tenant.OrgID, n identity.NodeID, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if o == orgX && n == nodeX {
		l.refusals = append(l.refusals, reason)
	}
}

func (l *liveSink) counts() (connected, disconnected, snapshots, refusals int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.connected), len(l.disconnected), len(l.snapshots), len(l.refusals)
}

func processSnapshot() *agentv1.SessionRequest {
	uid, cpu, cmd := uint32(33), 0.25, "nginx: worker\u202eprocess"
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_ProcessSnapshot{ProcessSnapshot: &agentv1.ProcessSnapshot{
		TakenAt: timestamppb.New(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)),
		Summary: &agentv1.SystemSummary{CpuCount: 4, MemoryTotalBytes: 8 << 30, Tasks: &agentv1.TaskCounts{Total: 1, Sleeping: 1}},
		Processes: []*agentv1.ProcessInfo{{
			Pid: 812, Ppid: 1, Uid: &uid, User: "www-data", State: "S", CpuRatio: &cpu, Threads: 2,
			Name: "nginx", Cmdline: &cmd, Unit: "nginx.service", Container: "4f1c0a5e8b2d",
		}},
		Omitted: 3,
	}}}
}

// KONZEPT §9, M3-14: the hub starts and stops the live view through the node's session; the
// session passes snapshots and refusals on, with invisible characters made visible.
func TestLiveView_CommandsReachTheAgentAndSnapshotsTheHub(t *testing.T) {
	sink := &liveSink{}
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithLiveView(sink)})
	if s.sessions.StartLiveView(orgX, nodeX, liveview.Interval, false) {
		t.Error("started without a session")
	}
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c, _, _, _ := sink.counts(); return c == 1 })
	if len(s.sessions.Active()) != 1 {
		t.Fatal("connected before the session was registered")
	}
	if s.sessions.StartLiveView(tenant.OrgID(uuid.New()), nodeX, liveview.Interval, false) {
		t.Error("started for another organization")
	}
	if !s.sessions.StartLiveView(orgX, nodeX, liveview.Interval, true) {
		t.Fatal("not started")
	}
	res, err := receive(stream)
	start := res.GetStartLiveView()
	if err != nil || start == nil || start.GetInterval().AsDuration() != 2*time.Second || !start.GetHideCmdline() {
		t.Fatalf("Receive() = %v, %v; want StartLiveView", res, err)
	}
	s.sessions.StopLiveView(orgX, nodeX)
	if res, err := stream.Receive(); err != nil || res.GetStopLiveView() == nil {
		t.Fatalf("Receive() = %v, %v; want StopLiveView", res, err)
	}

	// M3-29: a snapshot nobody here wants is not even converted.
	sink.mu.Lock()
	sink.skip = 1
	sink.mu.Unlock()
	for range 2 {
		if err := stream.Send(processSnapshot()); err != nil {
			t.Fatal(err)
		}
	}
	refused := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_LiveViewRefused{LiveViewRefused: &agentv1.LiveViewRefused{Reason: "agent.toml\x1b[31m forbids"}}}
	if err := stream.Send(refused); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, _, n, r := sink.counts(); return n == 1 && r == 1 })
	sink.mu.Lock()
	got, reason, asked := sink.snapshots[0], sink.refusals[0], sink.asked
	sink.mu.Unlock()
	if asked != 2 {
		t.Errorf("Wants asked %d times for two snapshots", asked)
	}
	p := got.Processes[0]
	if got.Omitted != 3 || got.Summary.CPUCount != 4 || got.Summary.Tasks.Sleeping != 1 || !got.TakenAt.Equal(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)) ||
		p.PID != 812 || *p.UID != 33 || p.User != "www-data" || *p.CPURatio != 0.25 || p.Unit != "nginx.service" || p.Container != "4f1c0a5e8b2d" ||
		p.RSSBytes != nil || p.Name != "nginx" || p.State != "S" {
		t.Errorf("snapshot = %+v, process %+v", got, p)
	}
	if *p.Cmdline != "nginx: worker\uFFFDprocess" || reason != "agent.toml\uFFFD[31m forbids" {
		t.Errorf("invisible characters passed: %q, %q", *p.Cmdline, reason)
	}

	_ = stream.CloseRequest()
	waitFor(t, func() bool { _, d, _, _ := sink.counts(); return d == 1 })
	if s.sessions.StartLiveView(orgX, nodeX, liveview.Interval, false) {
		t.Error("started after the session ended")
	}
}

// A reconnect replaces the old session: its end must not report the node offline.
func TestLiveView_AReplacedSessionDoesNotReportOffline(t *testing.T) {
	sink := &liveSink{}
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithLiveView(sink)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	client := s.client(t, &cert, tls.VersionTLS13)
	first := client.Session(within(t))
	if err := first.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c, _, _, _ := sink.counts(); return c == 1 })
	second := client.Session(within(t))
	if err := second.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c, _, _, _ := sink.counts(); return c == 2 })
	time.Sleep(100 * time.Millisecond)
	if _, d, _, _ := sink.counts(); d != 0 {
		t.Errorf("%d offline reports after a replacement", d)
	}
	_ = second.CloseRequest()
	waitFor(t, func() bool { _, d, _, _ := sink.counts(); return d == 1 })
}
