package agentapi

import (
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
)

// LiveViews takes what the sessions report for the live view (liveview.Hub, KONZEPT §9, M3-14).
type LiveViews interface {
	// Connected: the node's session is open on this instance.
	Connected(org tenant.OrgID, node identity.NodeID)
	// Disconnected: the node's session on this instance ended (not when a reconnect replaced it).
	Disconnected(org tenant.OrgID, node identity.NodeID)
	// Wants reports whether anyone here watches the node; else its snapshots are dropped
	// unconverted (M3-29).
	Wants(org tenant.OrgID, node identity.NodeID) bool
	Deliver(org tenant.OrgID, node identity.NodeID, snapshot liveview.Snapshot)
	Refused(org tenant.OrgID, node identity.NodeID, reason string)
}

// WithLiveView passes snapshots and refusals of the agents to views; without it they are dropped.
func WithLiveView(views LiveViews) HandlerOption {
	return func(h *SessionHandler) { h.live = views }
}

var _ liveview.Agents = (*Sessions)(nil)

// liveCommand is a start (or renewal) or a stop of the live view.
type liveCommand struct {
	stop     bool
	interval time.Duration
	hide     bool
}

// StartLiveView starts or renews the node's live view (liveview.Agents); false without a session
// of the node on this instance.
func (s *Sessions) StartLiveView(org tenant.OrgID, node identity.NodeID, interval time.Duration, hideCmdline bool) bool {
	return s.sendLive(org, node, liveCommand{interval: interval, hide: hideCmdline})
}

// StopLiveView ends the node's live view (liveview.Agents).
func (s *Sessions) StopLiveView(org tenant.OrgID, node identity.NodeID) {
	s.sendLive(org, node, liveCommand{stop: true})
}

// sendLive queues the command; a queued one not yet sent is replaced (only the newest counts).
func (s *Sessions) sendLive(org tenant.OrgID, node identity.NodeID, cmd liveCommand) bool {
	s.mu.Lock()
	l := s.active[node]
	s.mu.Unlock()
	if l == nil || l.Org != org {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.liveCommands:
	default:
	}
	l.liveCommands <- cmd
	return true
}

func liveResponse(cmd liveCommand) *agentv1.SessionResponse {
	if cmd.stop {
		return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_StopLiveView{StopLiveView: &agentv1.StopLiveView{}}}
	}
	return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_StartLiveView{StartLiveView: &agentv1.StartLiveView{
		Interval: durationpb.New(cmd.interval), HideCmdline: cmd.hide,
	}}}
}

// sendLive sends a queued command to the agent.
func sendLive(stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse], cmd liveCommand) error {
	return stream.Send(liveResponse(cmd)) //nolint:wrapcheck // connect errors must reach the client unchanged
}

// toSnapshot converts the agent's snapshot; every text from the host is made visible.
func toSnapshot(s *agentv1.ProcessSnapshot) liveview.Snapshot {
	sum := s.GetSummary()
	tasks := sum.GetTasks()
	out := liveview.Snapshot{
		TakenAt: s.GetTakenAt().AsTime(),
		Summary: liveview.Summary{
			CPUBusyRatio: sum.CpuBusyRatio, CPUCount: sum.GetCpuCount(),
			MemoryTotalBytes: sum.GetMemoryTotalBytes(), MemoryAvailable: sum.GetMemoryAvailableBytes(),
			SwapTotalBytes: sum.GetSwapTotalBytes(), SwapFreeBytes: sum.GetSwapFreeBytes(),
			Load1: sum.Load_1, Load5: sum.Load_5, Load15: sum.Load_15, UptimeSeconds: sum.UptimeSeconds,
			Tasks: liveview.Tasks{
				Total: tasks.GetTotal(), Running: tasks.GetRunning(), Sleeping: tasks.GetSleeping(),
				Stopped: tasks.GetStopped(), Zombie: tasks.GetZombie(),
			},
		},
		Processes: make([]liveview.Process, 0, len(s.GetProcesses())),
		Omitted:   s.GetOmitted(),
	}
	for _, p := range s.GetProcesses() {
		var cmdline *string
		if p.Cmdline != nil {
			visible := textsafe.Visible(p.GetCmdline())
			cmdline = &visible
		}
		out.Processes = append(out.Processes, liveview.Process{
			PID: p.GetPid(), PPID: p.GetPpid(), UID: p.Uid,
			User: textsafe.Visible(p.GetUser()), State: textsafe.Visible(p.GetState()),
			CPURatio: p.CpuRatio, RSSBytes: p.RssBytes, MemoryRatio: p.MemoryRatio,
			Threads: p.GetThreads(), RuntimeSeconds: p.RuntimeSeconds, Name: textsafe.Visible(p.GetName()),
			Cmdline: cmdline, CmdlineTruncated: p.GetCmdlineTruncated(), CmdlineHidden: p.GetCmdlineHidden(),
			KernelThread: p.GetKernelThread(), Unit: textsafe.Visible(p.GetUnit()), Container: p.GetContainer(),
		})
	}
	return out
}
