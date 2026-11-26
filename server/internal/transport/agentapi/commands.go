package agentapi

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
)

// CommandPoll is how often a session looks for commands without a wake-up: the fallback when a
// NOTIFY of another instance went missing (M4-11).
const CommandPoll = 30 * time.Second

// Commands are the server's commands to the agents (M4-11). Organization and node come from the
// certificate.
type Commands interface {
	// Deliverable returns the node's commands to send: the pending ones, with resend (a new
	// session) the sent ones without result too.
	Deliverable(ctx context.Context, org tenant.OrgID, node identity.NodeID, resend bool, now time.Time) ([]command.Command, error)
	// MarkSent records what went out.
	MarkSent(ctx context.Context, org tenant.OrgID, node identity.NodeID, ids []uuid.UUID, now time.Time) error
}

// WithCommands delivers the server's commands to the agents; without it no command goes out.
func WithCommands(c Commands, poll time.Duration) HandlerOption {
	return func(h *SessionHandler) { h.commands, h.commandPoll = c, poll }
}

// CommandsWaiting wakes the session of node on this instance, if it has one: a command for it was
// stored (by any instance, through NOTIFY).
func (s *Sessions) CommandsWaiting(node identity.NodeID) {
	s.mu.Lock()
	l := s.active[node]
	s.mu.Unlock()
	if l == nil {
		return
	}
	select {
	case l.nodeCommands <- struct{}{}:
	default: // a look is already queued
	}
}

// commandMessage is the protocol message of a command at now, with the time the agent may still
// start a run (M4-31); false for a run with less than a second left.
func commandMessage(c command.Command, now time.Time) (*agentv1.SessionResponse, bool) {
	run, valid := c.ID.String(), c.ValidFor(now)
	if valid == 0 && c.Kind != command.Refresh {
		return nil, false
	}
	switch c.Kind {
	case command.Update:
		scope := agentv1.UpdateScope_UPDATE_SCOPE_SECURITY
		if c.Scope == command.ScopeAll {
			scope = agentv1.UpdateScope_UPDATE_SCOPE_ALL
		}
		return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_RunUpdate{RunUpdate: &agentv1.RunUpdate{RunId: run, Scope: scope, ValidForSeconds: valid}}}, true
	case command.Reboot:
		return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_RebootNode{RebootNode: &agentv1.RebootNode{RunId: run, Reason: c.Reason, ValidForSeconds: valid}}}, true
	case command.SelfUpdate:
		return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_SelfUpdate{SelfUpdate: &agentv1.SelfUpdate{RunId: run, Version: c.Version, ValidForSeconds: valid}}}, true
	case command.Refresh:
	}
	return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_RefreshUpdateInfo{RefreshUpdateInfo: &agentv1.RefreshUpdateInfo{}}}, true
}

// deliverCommands sends the node's waiting commands and records them as sent. A store that fails
// is logged: the next look tries again, and the agent runs a run ID once.
func (h *SessionHandler) deliverCommands(ctx context.Context, id Identity, resend bool, stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse]) error {
	if h.commands == nil {
		return nil
	}
	waiting, err := h.commands.Deliverable(ctx, id.Org, id.Node, resend, time.Now())
	if err != nil {
		h.logger.WarnContext(ctx, "read commands failed", "node", id.Node.String(), "error", err)
		return nil
	}
	if len(waiting) == 0 {
		return nil
	}
	sent := make([]uuid.UUID, 0, len(waiting))
	for _, c := range waiting {
		msg, ok := commandMessage(c, time.Now())
		if !ok {
			continue
		}
		if err := stream.Send(msg); err != nil {
			return err //nolint:wrapcheck // connect errors must reach the client unchanged
		}
		sent = append(sent, c.ID)
		h.logger.InfoContext(ctx, "command sent", "node", id.Node.String(), "run_id", c.ID.String(), "kind", int(c.Kind))
	}
	if err := h.commands.MarkSent(ctx, id.Org, id.Node, sent, time.Now()); err != nil {
		h.logger.WarnContext(ctx, "record sent commands failed", "node", id.Node.String(), "error", err)
	}
	return nil
}

var phases = map[agentv1.CommandPhase]command.Phase{
	agentv1.CommandPhase_COMMAND_PHASE_STARTED:     command.Started,
	agentv1.CommandPhase_COMMAND_PHASE_SUCCEEDED:   command.Succeeded,
	agentv1.CommandPhase_COMMAND_PHASE_FAILED:      command.Failed,
	agentv1.CommandPhase_COMMAND_PHASE_REFUSED:     command.Refused,
	agentv1.CommandPhase_COMMAND_PHASE_UNSUPPORTED: command.Unsupported,
	agentv1.CommandPhase_COMMAND_PHASE_LOST:        command.Lost,
}

// toResult converts a validated command result (the run ID's form is checked by the schema).
// The host's times are capped at the server's (review M4-30): shown and stored, they say nothing
// a host could use to hold a rollout.
func toResult(r *agentv1.CommandResult) *ingest.Result {
	return toResultAt(r, time.Now())
}

func toResultAt(r *agentv1.CommandResult, now time.Time) *ingest.Result {
	run, err := uuid.Parse(r.GetRunId())
	if err != nil {
		return nil
	}
	out := &ingest.Result{Run: run, Result: command.Result{
		Phase: phases[r.GetPhase()], ExitCode: r.GetExitCode(), Reason: r.GetReason(), Output: r.GetOutput(),
		OutputTruncated: r.GetOutputTruncated(),
	}}
	if r.GetEndedAt() != nil {
		at := minTime(r.GetEndedAt().AsTime(), now)
		out.EndedAt = &at
	}
	if r.GetStartedAt() != nil {
		at := minTime(r.GetStartedAt().AsTime(), now)
		if out.EndedAt != nil {
			at = minTime(at, *out.EndedAt)
		}
		out.StartedAt = &at
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
