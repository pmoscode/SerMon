package agentapi

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

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
