package agentapi_test

import (
	"context"
	"crypto/tls"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
)

// fakeCommands behaves like the store: pending ones go out, after a reconnect also sent ones.
type fakeCommands struct {
	mu       sync.Mutex
	commands []command.Command
	looked   []identity.NodeID
	results  map[uuid.UUID][]command.Result
	// cancels are the sent runs withdrawn; cancelSent those whose cancel went out (M4-31).
	cancels    []uuid.UUID
	cancelSent map[uuid.UUID]bool
}

func (f *fakeCommands) Cancels(_ context.Context, _ tenant.OrgID, _ identity.NodeID, resend bool) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []uuid.UUID
	for _, id := range f.cancels {
		if resend || !f.cancelSent[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeCommands) MarkCancelsSent(_ context.Context, _ tenant.OrgID, _ identity.NodeID, ids []uuid.UUID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelSent == nil {
		f.cancelSent = map[uuid.UUID]bool{}
	}
	for _, id := range ids {
		f.cancelSent[id] = true
	}
	return nil
}

func (f *fakeCommands) add(c command.Command) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.State = command.Pending
	f.commands = append(f.commands, c)
}

func (f *fakeCommands) Deliverable(_ context.Context, _ tenant.OrgID, node identity.NodeID, resend bool, now time.Time) ([]command.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.looked = append(f.looked, node)
	var out []command.Command
	for _, c := range f.commands {
		if c.Node == node && (c.State == command.Pending || resend && c.State == command.Sent) && now.Before(c.ExpiresAt) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeCommands) MarkSent(_ context.Context, _ tenant.OrgID, _ identity.NodeID, ids []uuid.UUID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.commands {
		for _, id := range ids {
			if f.commands[i].ID == id {
				f.commands[i].State = command.Sent
			}
		}
	}
	return nil
}

func (f *fakeCommands) Record(_ context.Context, _ tenant.OrgID, node identity.NodeID, run uuid.UUID, r command.Result) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = map[uuid.UUID][]command.Result{}
	}
	for _, c := range f.commands {
		if c.ID == run && c.Node == node {
			f.results[run] = append(f.results[run], r)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeCommands) recorded(run uuid.UUID) []command.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]command.Result(nil), f.results[run]...)
}

func newRun(kind command.Kind, node identity.NodeID, expires time.Duration) command.Command {
	now := time.Now()
	return command.Command{
		ID: uuid.Must(uuid.NewV7()), Node: node, Kind: kind, Scope: command.ScopeSecurity, Reason: "updates (SerMon)",
		CreatedAt: now, ExpiresAt: now.Add(expires),
	}
}

// nextCommand receives until a command of the server arrives.
func nextCommand(t *testing.T, stream interface {
	Receive() (*agentv1.SessionResponse, error)
},
) *agentv1.SessionResponse {
	t.Helper()
	for {
		res, err := stream.Receive()
		if err != nil {
			t.Fatalf("Receive() = %v", err)
		}
		switch res.GetPayload().(type) {
		case *agentv1.SessionResponse_RunUpdate, *agentv1.SessionResponse_RebootNode, *agentv1.SessionResponse_SelfUpdate,
			*agentv1.SessionResponse_RefreshUpdateInfo, *agentv1.SessionResponse_CancelRun:
			return res
		}
	}
}

// M4-11: a session sends the node's waiting commands at its start, a wake-up (NOTIFY of any
// instance) sends new ones at once, a reconnect sends the sent ones without result again, an
// expired one never goes out; the agent's results land under the certificate's node.
func TestSession_DeliversTheNodesCommands(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	config := ingest.DefaultConfig()
	config.FlushInterval = 20 * time.Millisecond
	service := ingest.NewService(store, metrics.NewRing(), fakeNodes{group: group}, config, time.Now, slog.New(slog.DiscardHandler))
	commands := &fakeCommands{}
	service.SetResultWriter(commands)
	update, expired := newRun(command.Update, nodeX, time.Hour), newRun(command.Reboot, nodeX, -time.Second)
	foreign := newRun(command.Update, identity.NodeID(uuid.New()), time.Hour)
	commands.add(update)
	commands.add(expired)
	commands.add(foreign)
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{
		agentapi.WithIngest(service), agentapi.WithCommands(commands, time.Hour),
	})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	client := s.client(t, &cert, tls.VersionTLS13)
	ctx, cancel := context.WithCancel(context.Background())
	stream := client.Session(ctx)
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	got := nextCommand(t, stream).GetRunUpdate()
	if got.GetRunId() != update.ID.String() || got.GetScope() != agentv1.UpdateScope_UPDATE_SCOPE_SECURITY ||
		got.GetValidForSeconds() < 3500 || got.GetValidForSeconds() > 3600 {
		t.Fatalf("first command = %v", got)
	}
	// A new command and a wake-up: it goes out at once (the poll is an hour away).
	reboot := newRun(command.Reboot, nodeX, time.Hour)
	commands.add(reboot)
	s.sessions.CommandsWaiting(nodeX)
	if r := nextCommand(t, stream).GetRebootNode(); r.GetRunId() != reboot.ID.String() || r.GetReason() != "updates (SerMon)" {
		t.Fatalf("after the wake-up = %v", r)
	}
	// The agent reports the update's phases.
	started := time.Now().Truncate(time.Microsecond)
	for seq, phase := range map[uint64]agentv1.CommandPhase{4: agentv1.CommandPhase_COMMAND_PHASE_STARTED} {
		if err := stream.Send(&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
			Seq: seq, Content: &agentv1.Batch_CommandResult{CommandResult: &agentv1.CommandResult{
				RunId: update.ID.String(), Phase: phase, StartedAt: timestamppb.New(started),
			}},
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	for {
		res, err := stream.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if res.GetAck().GetSeq() == 4 {
			break
		}
	}
	if r := commands.recorded(update.ID); len(r) != 1 || r[0].Phase != command.Started || r[0].StartedAt == nil || !r[0].StartedAt.Equal(started) {
		t.Errorf("recorded = %+v", r)
	}
	cancel()

	// Reconnect: both sent commands again, nothing expired or foreign.
	again := client.Session(context.Background())
	defer func() { _ = again.CloseRequest() }()
	if err := again.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		res := nextCommand(t, again)
		seen[res.GetRunUpdate().GetRunId()+res.GetRebootNode().GetRunId()] = true
	}
	if !seen[update.ID.String()] || !seen[reboot.ID.String()] {
		t.Errorf("after the reconnect: %v", seen)
	}
	// The session marks what it sent meanwhile: read under the fake's lock.
	commands.mu.Lock()
	defer commands.mu.Unlock()
	for _, c := range commands.commands {
		if c.ID == expired.ID && c.State != command.Pending || c.ID == foreign.ID && c.State != command.Pending {
			t.Errorf("%v went out: %+v", c.ID, c)
		}
	}
}

// Without a wake-up the session looks every poll interval (another instance's NOTIFY may get lost).
func TestSession_LooksForCommandsWithoutAWakeUp(t *testing.T) {
	commands := &fakeCommands{}
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithCommands(commands, 50*time.Millisecond)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	defer func() { _ = stream.CloseRequest() }()
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Receive(); err != nil { // the trust bundle: the session runs
		t.Fatal(err)
	}
	refresh := newRun(command.Refresh, nodeX, time.Hour)
	commands.add(refresh)
	if res := nextCommand(t, stream); res.GetRefreshUpdateInfo() == nil {
		t.Fatalf("got %v, want the refresh", res)
	}
}

// M4-31: a sent run withdrawn on the server goes to the agent as CancelRun after a wake-up, once
// per session, and again after a reconnect until the agent answers.
func TestSession_WithdrawsCancelledRuns(t *testing.T) {
	commands := &fakeCommands{}
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithCommands(commands, time.Hour)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	client := s.client(t, &cert, tls.VersionTLS13)
	ctx, cancel := context.WithCancel(context.Background())
	stream := client.Session(ctx)
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Receive(); err != nil {
		t.Fatal(err)
	}
	run := uuid.Must(uuid.NewV7())
	commands.mu.Lock()
	commands.cancels = []uuid.UUID{run}
	commands.mu.Unlock()
	s.sessions.CommandsWaiting(nodeX)
	if got := nextCommand(t, stream).GetCancelRun(); got.GetRunId() != run.String() {
		t.Fatalf("got %v, want the cancel of %v", got, run)
	}
	cancel()
	again := client.Session(context.Background())
	defer func() { _ = again.CloseRequest() }()
	if err := again.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if got := nextCommand(t, again).GetCancelRun(); got.GetRunId() != run.String() {
		t.Fatalf("after the reconnect: %v", got)
	}
}
