// Package command is a command of the server to an agent's root helper (KONZEPT §14, M4-11):
// refresh the update information, install updates, reboot, replace the agent. The server picks
// the run ID; the agent runs a command once and reports its phases (CommandResult).
package command

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// Kind is what the command does.
type Kind uint8

// The kinds.
const (
	Refresh Kind = iota + 1
	Update
	Reboot
	SelfUpdate
)

// Scope is which updates a Kind Update installs.
type Scope uint8

// The scopes.
const (
	ScopeSecurity Scope = iota + 1
	ScopeAll
)

// State is where the command is on the server.
type State uint8

// The states.
const (
	// Pending waits for the node's session.
	Pending State = iota + 1
	// Sent went out in a session; after a reconnect it goes out again (the agent runs it once).
	Sent
	// Running: the agent reported the start.
	Running
	// Done: a final phase arrived (or, for a refresh, it was sent).
	Done
	// Expired: the node didn't take it in time.
	Expired
)

// Phase is how far the agent says it came (sermon.agent.v1.CommandPhase).
type Phase uint8

// The phases.
const (
	PhaseUnknown Phase = iota
	Started
	Succeeded
	Failed
	Refused
	Unsupported
	Lost
)

// Final reports whether no later phase follows.
func (p Phase) Final() bool { return p > Started && p <= Lost }

// Bounds of a command and its result.
const (
	MaxReason = 200
	MaxOutput = 256 << 10
	MaxResult = 1024
	// DefaultExpiry is how long a command waits for its node.
	DefaultExpiry = 15 * time.Minute
	// SentGrace is how long a sent command waits past its expiry for the agent's answer (M4-31):
	// the agent starts no run after the expiry (valid_for_seconds), but a start just in time or
	// the report that its time ran out may still be on its way, buffered while offline.
	SentGrace = 15 * time.Minute
)

// Expiry is when a command made at now expires: after DefaultExpiry, at the end of a rollout's
// window at the latest (M4-31) – nothing of it starts on the host later.
func Expiry(now, windowEnd time.Time) time.Time {
	if at := now.Add(DefaultExpiry); at.Before(windowEnd) {
		return at
	}
	return windowEnd
}

// ErrInvalid means a command is malformed.
var ErrInvalid = errors.New("invalid command")

// Command is one command to one node.
type Command struct {
	ID    uuid.UUID // the run ID the agent sees
	Node  identity.NodeID
	Kind  Kind
	Scope Scope // Update only
	// Reason is shown on the host for a Reboot: 1 to 200 bytes, no control characters.
	Reason string
	// Version is the agent version of a SelfUpdate.
	Version   string
	State     State
	CreatedAt time.Time
	ExpiresAt time.Time
	CreatedBy identity.UserID // zero for the orchestrator
	SentAt    *time.Time
	Result    *Result
}

// Validate checks what a command needs for its kind.
func (c Command) Validate() error {
	switch {
	case c.ID == uuid.Nil || c.ID.Version() != 7 && c.ID.Version() != 4:
		return errors.Join(ErrInvalid, errors.New("the run ID is a random or time-ordered UUID"))
	case c.Kind < Refresh || c.Kind > SelfUpdate:
		return errors.Join(ErrInvalid, errors.New("unknown kind"))
	case c.Kind == Update && c.Scope != ScopeSecurity && c.Scope != ScopeAll:
		return errors.Join(ErrInvalid, errors.New("an update needs the scope security or all"))
	case c.Kind == Reboot && !validReason(c.Reason):
		return errors.Join(ErrInvalid, errors.New("a reboot needs a reason of 1 to 200 bytes without control characters"))
	case c.Kind == SelfUpdate && (c.Version == "" || len(c.Version) > 64):
		return errors.Join(ErrInvalid, errors.New("a self-update needs a version"))
	case !c.ExpiresAt.After(c.CreatedAt):
		return errors.Join(ErrInvalid, errors.New("a command expires after its creation"))
	}
	return nil
}

func validReason(s string) bool {
	if s == "" || len(s) > MaxReason || !utf8.ValidString(s) {
		return false
	}
	// No control, format or separator characters (review M4-30): the reason reaches the host's
	// terminals, its journal and the audit log, where a bidi override would forge what it says.
	return !strings.ContainsFunc(s, textsafe.Invisible)
}

// Result is what the agent reported of a run.
type Result struct {
	Phase    Phase
	ExitCode int32
	// Reason explains a refusal or failure; Output is the end of the output, redacted by the agent.
	Reason, Output  string
	OutputTruncated bool
	StartedAt       *time.Time
	EndedAt         *time.Time
}

// EffectiveState is the state at now: a pending command expires at its expiry, a sent one
// SentGrace later without an answer of the agent.
func (c Command) EffectiveState(now time.Time) State {
	switch {
	case c.State == Pending && !now.Before(c.ExpiresAt), c.State == Sent && !now.Before(c.ExpiresAt.Add(SentGrace)):
		return Expired
	}
	return c.State
}

// ValidFor is how long the agent may still start the run, in whole seconds (valid_for_seconds).
func (c Command) ValidFor(now time.Time) uint32 {
	left := c.ExpiresAt.Sub(now)
	if left < time.Second {
		return 0
	}
	return uint32(min(left/time.Second, 86400))
}
