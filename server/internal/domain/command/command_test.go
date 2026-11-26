package command_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/command"
)

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func valid(kind command.Kind) command.Command {
	return command.Command{
		ID: uuid.Must(uuid.NewV7()), Kind: kind, Scope: command.ScopeSecurity, Reason: "updates (SerMon)", Version: "0.5.0",
		CreatedAt: t0, ExpiresAt: t0.Add(command.DefaultExpiry),
	}
}

func TestValidate(t *testing.T) {
	for _, k := range []command.Kind{command.Refresh, command.Update, command.Reboot, command.SelfUpdate} {
		if err := valid(k).Validate(); err != nil {
			t.Errorf("kind %d: %v", k, err)
		}
	}
	for name, change := range map[string]func(*command.Command){
		"no id":          func(c *command.Command) { c.ID = uuid.Nil },
		"made-up id":     func(c *command.Command) { c.ID = uuid.MustParse("00000000-0000-1000-8000-000000000001") },
		"kind":           func(c *command.Command) { c.Kind = 9 },
		"no kind":        func(c *command.Command) { c.Kind = 0 },
		"scope":          func(c *command.Command) { c.Kind, c.Scope = command.Update, 0 },
		"scope 3":        func(c *command.Command) { c.Kind, c.Scope = command.Update, 3 },
		"no reason":      func(c *command.Command) { c.Kind, c.Reason = command.Reboot, "" },
		"long reason":    func(c *command.Command) { c.Kind, c.Reason = command.Reboot, strings.Repeat("x", 201) },
		"control reason": func(c *command.Command) { c.Kind, c.Reason = command.Reboot, "now\n-h" },
		"broken reason":  func(c *command.Command) { c.Kind, c.Reason = command.Reboot, "\xff" },
		// Review M4-30: format and separator characters, too.
		"bidi reason":      func(c *command.Command) { c.Kind, c.Reason = command.Reboot, "ok\u202etxet" },
		"separator reason": func(c *command.Command) { c.Kind, c.Reason = command.Reboot, "a\u2028b" },
		"no version":       func(c *command.Command) { c.Kind, c.Version = command.SelfUpdate, "" },
		"long version":     func(c *command.Command) { c.Kind, c.Version = command.SelfUpdate, strings.Repeat("1", 65) },
		"expired":          func(c *command.Command) { c.ExpiresAt = c.CreatedAt },
	} {
		c := valid(command.Refresh)
		change(&c)
		if err := c.Validate(); !errors.Is(err, command.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	reason := valid(command.Reboot)
	reason.Reason = strings.Repeat("ä", 100)
	if err := reason.Validate(); err != nil {
		t.Errorf("200 bytes: %v", err)
	}
}

func TestPhaseFinal(t *testing.T) {
	for p, final := range map[command.Phase]bool{
		command.PhaseUnknown: false, command.Started: false, command.Succeeded: true, command.Failed: true,
		command.Refused: true, command.Unsupported: true, command.Lost: true, 7: false,
	} {
		if p.Final() != final {
			t.Errorf("phase %d final = %v", p, p.Final())
		}
	}
}

func TestEffectiveState_AWaitingCommandExpires(t *testing.T) {
	c := valid(command.Update)
	for state, want := range map[command.State]command.State{
		command.Pending: command.Expired, command.Running: command.Running, command.Done: command.Done,
	} {
		c.State = state
		if got := c.EffectiveState(c.ExpiresAt); got != want {
			t.Errorf("%d at expiry = %d, want %d", state, got, want)
		}
		if got := c.EffectiveState(c.ExpiresAt.Add(-time.Nanosecond)); got != state {
			t.Errorf("%d before expiry = %d", state, got)
		}
	}
}

// M4-31: a sent command doesn't expire silently while the agent may answer: the agent starts
// nothing after the expiry (valid_for_seconds), but its answer – a start just in time, or that it
// ran out of time – may still be on its way.
func TestEffectiveState_ASentCommandWaitsForTheAnswer(t *testing.T) {
	c := valid(command.Update)
	c.State = command.Sent
	if got := c.EffectiveState(c.ExpiresAt.Add(command.SentGrace - time.Nanosecond)); got != command.Sent {
		t.Errorf("within the grace: %d", got)
	}
	if got := c.EffectiveState(c.ExpiresAt.Add(command.SentGrace)); got != command.Expired {
		t.Errorf("after the grace: %d", got)
	}
}

// M4-31: the agent gets the time left until the expiry, in whole seconds; nothing once it passed.
func TestValidFor_IsTheTimeLeft(t *testing.T) {
	c := valid(command.Update)
	if got := c.ValidFor(c.ExpiresAt.Add(-90*time.Second - 500*time.Millisecond)); got != 90 {
		t.Errorf("90.5 s left: %d", got)
	}
	if got := c.ValidFor(c.ExpiresAt.Add(time.Second)); got != 0 {
		t.Errorf("after the expiry: %d", got)
	}
}

// M4-31: a rollout's command expires at the end of its window at the latest.
func TestExpiry_EndsWithTheWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 4, 50, 0, 0, time.UTC)
	if got := command.Expiry(now, now.Add(10*time.Minute)); !got.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("10 min before the end: %v", got)
	}
	if got := command.Expiry(now, now.Add(time.Hour)); !got.Equal(now.Add(command.DefaultExpiry)) {
		t.Errorf("an hour before the end: %v", got)
	}
}
