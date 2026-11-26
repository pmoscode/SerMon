package agentapi

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/domain/command"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
)

// Review M4-30: a host's clock can't push a run's times past the server's.
func TestToResult_CapsTheHostsTimes(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	result := func(started, ended time.Time) *agentv1.CommandResult {
		return &agentv1.CommandResult{
			RunId: "0193f5a0-0000-7000-8000-000000000001", Phase: agentv1.CommandPhase_COMMAND_PHASE_SUCCEEDED,
			StartedAt: timestamppb.New(started), EndedAt: timestamppb.New(ended),
		}
	}
	r := toResultAt(result(now.Add(-time.Minute), now.Add(-time.Second)), now)
	if !r.StartedAt.Equal(now.Add(-time.Minute)) || !r.EndedAt.Equal(now.Add(-time.Second)) {
		t.Errorf("plausible times changed: %v %v", r.StartedAt, r.EndedAt)
	}
	future := now.Add(365 * 24 * time.Hour)
	r = toResultAt(result(future.Add(time.Hour), future), now)
	if !r.EndedAt.Equal(now) || !r.StartedAt.Equal(now) {
		t.Errorf("future times = %v %v", r.StartedAt, r.EndedAt)
	}
	r = toResultAt(result(now.Add(-time.Minute), now.Add(-2*time.Minute)), now)
	if r.StartedAt.After(*r.EndedAt) {
		t.Errorf("started after it ended: %v %v", r.StartedAt, r.EndedAt)
	}
}

// M4-31: every run goes out with the time the agent may still start it; one with less than a
// second left doesn't go out at all (it would start after the server gave up on it).
func TestCommandMessage_CarriesTheTimeLeft(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for _, kind := range []command.Kind{command.Update, command.Reboot, command.SelfUpdate} {
		c := command.Command{ID: uuid.New(), Kind: kind, Scope: command.ScopeAll, Reason: "x", Version: "1.0.0", ExpiresAt: now.Add(10 * time.Minute)}
		msg, ok := commandMessage(c, now)
		var valid uint32
		switch p := msg.GetPayload().(type) {
		case *agentv1.SessionResponse_RunUpdate:
			valid = p.RunUpdate.GetValidForSeconds()
		case *agentv1.SessionResponse_RebootNode:
			valid = p.RebootNode.GetValidForSeconds()
		case *agentv1.SessionResponse_SelfUpdate:
			valid = p.SelfUpdate.GetValidForSeconds()
		}
		if !ok || valid != 600 {
			t.Errorf("kind %d: valid for %d s (%v), want 600", kind, valid, ok)
		}
		if _, ok := commandMessage(c, c.ExpiresAt.Add(-500*time.Millisecond)); ok {
			t.Errorf("kind %d with half a second left went out", kind)
		}
	}
	refresh := command.Command{ID: uuid.New(), Kind: command.Refresh, ExpiresAt: now.Add(time.Minute)}
	if msg, ok := commandMessage(refresh, now); !ok || msg.GetRefreshUpdateInfo() == nil {
		t.Errorf("refresh = %v, %v", msg, ok)
	}
}
