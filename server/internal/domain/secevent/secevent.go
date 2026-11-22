// Package secevent is the domain of security events (KONZEPT §11, M2-07): what the agent parsed
// from the host's logs (ssh logins, sudo, accounts, OOM kills, reboots), as stored and queried
// by the server. Fields an attacker can choose are bounded here, once for ingest and storage.
package secevent

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

// ErrInvalid means an event or a filter is malformed.
var ErrInvalid = errors.New("invalid security event")

// Kind is the type of an event.
type Kind string

// Kinds (names as in KONZEPT §11).
const (
	SSHLoginFailed       Kind = "ssh.login.failed"
	SSHInvalidUser       Kind = "ssh.login.invalid_user"
	SSHLoginSucceeded    Kind = "ssh.login.success"
	SSHPreauthDisconnect Kind = "ssh.preauth.disconnect"
	SudoAuthFailed       Kind = "sudo.auth.failed"
	SudoNotAllowed       Kind = "sudo.not_allowed"
	SudoCommand          Kind = "sudo.command"
	AccountCreated       Kind = "account.created"
	AccountDeleted       Kind = "account.deleted"
	SystemReboot         Kind = "system.reboot"
	OOMKill              Kind = "system.oom_kill"

	// Written by the server itself, never by an agent (M2-24, T3, T4): an enrollment token was
	// used (the node is the new one), a node was locked, a node rotated from a new address.
	EnrollmentTokenUsed Kind = "enrollment.token_used"
	NodeLocked          Kind = "node.locked"
	NodeRotationNewIP   Kind = "node.rotation_new_ip"
)

var kinds = []Kind{
	SSHLoginFailed, SSHInvalidUser, SSHLoginSucceeded, SSHPreauthDisconnect, SudoAuthFailed, SudoNotAllowed,
	SudoCommand, AccountCreated, AccountDeleted, SystemReboot, OOMKill,
	EnrollmentTokenUsed, NodeLocked, NodeRotationNewIP,
}

// Kinds returns every kind.
func Kinds() []Kind { return append([]Kind(nil), kinds...) }

// ParseKind returns the kind of its name.
func ParseKind(name string) (Kind, error) {
	for _, k := range kinds {
		if string(k) == name {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: unknown kind %q", ErrInvalid, name)
}

// MaxAge is how old an event may be when it arrives (the ingest's default); the alert engine reads
// partitions this far back.
const MaxAge = 7 * 24 * time.Hour

// Bounds of the text fields (bytes), as in the agent protocol.
const (
	MaxUser    = 128
	MaxHost    = 253
	MaxShort   = 128
	MaxCommand = 1024
	MaxProcess = 64
	MaxReason  = 200 // as nodes.lock_reason
)

// Remote is the other end of an SSH connection: an IP address or a host name, and the port
// (0: unknown).
type Remote struct {
	IP   netip.Addr
	Host string
	Port uint16
}

// Details are the fields of particular kinds; empty ones are omitted when stored.
type Details struct {
	Method         string         `json:"method,omitempty"` // ssh: password, publickey, keyboard-interactive, …
	InvalidUser    bool           `json:"invalid_user,omitempty"`
	KeyType        string         `json:"key_type,omitempty"`
	KeyFingerprint string         `json:"key_fingerprint,omitempty"`
	TTY            string         `json:"tty,omitempty"`
	Target         string         `json:"target,omitempty"`  // sudo: the account asked for
	Command        string         `json:"command,omitempty"` // sudo.command, redacted by the agent
	UID            *uint32        `json:"uid,omitempty"`
	PID            uint32         `json:"pid,omitempty"`
	Process        string         `json:"process,omitempty"`
	PreviousBootID string         `json:"previous_boot_id,omitempty"`
	UptimeBefore   *time.Duration `json:"uptime_before,omitempty"`
	// Server events (M2-24).
	TokenID    string `json:"token_id,omitempty"`    // enrollment.token_used
	Reason     string `json:"reason,omitempty"`      // node.locked
	PreviousIP string `json:"previous_ip,omitempty"` // node.rotation_new_ip
}

// Event is one security event of a node.
type Event struct {
	ID         int64 // set by the store
	Node       identity.NodeID
	OccurredAt time.Time // from the log line
	ReceivedAt time.Time // set by the store
	Kind       Kind
	User       string
	Remote     *Remote
	Details    Details
	// Unverified: from a classic log file whose sender anyone on the host could be (ADR 0025).
	Unverified bool
}

var hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*$`)

// Normalized returns the event with IPv4-mapped addresses as IPv4.
func (e Event) Normalized() Event {
	if e.Remote != nil {
		r := *e.Remote
		r.IP = r.IP.Unmap()
		e.Remote = &r
	}
	return e
}

// Validate checks kind, bounds and addresses.
func (e Event) Validate() error {
	switch {
	case e.Node == identity.NodeID{}:
		return fmt.Errorf("%w: no node", ErrInvalid)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("%w: no time", ErrInvalid)
	}
	if _, err := ParseKind(string(e.Kind)); err != nil {
		return err
	}
	d := e.Details
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"user", e.User, MaxUser},
		{"method", d.Method, MaxShort},
		{"key type", d.KeyType, MaxShort},
		{"key fingerprint", d.KeyFingerprint, MaxShort},
		{"tty", d.TTY, MaxShort},
		{"target", d.Target, MaxUser},
		{"command", d.Command, MaxCommand},
		{"process", d.Process, MaxProcess},
		{"boot id", d.PreviousBootID, MaxShort},
		{"token", d.TokenID, MaxShort},
		{"reason", d.Reason, MaxReason},
		{"previous ip", d.PreviousIP, MaxShort},
	} {
		if len(f.value) > f.max || !utf8.ValidString(f.value) {
			return fmt.Errorf("%w: %s longer than %d bytes or not UTF-8", ErrInvalid, f.name, f.max)
		}
	}
	if r := e.Remote; r != nil {
		switch {
		case r.IP.IsValid() && r.Host != "":
			return fmt.Errorf("%w: remote with address and host name", ErrInvalid)
		case r.IP.IsValid() && r.IP.Zone() != "":
			return fmt.Errorf("%w: address with zone", ErrInvalid)
		case !r.IP.IsValid() && (len(r.Host) > MaxHost || !hostName.MatchString(r.Host)):
			return fmt.Errorf("%w: remote without address or valid host name", ErrInvalid)
		}
	}
	return nil
}
