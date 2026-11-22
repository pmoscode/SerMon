package secevent_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
)

var (
	node = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	t0   = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
)

func failed() secevent.Event {
	return secevent.Event{
		Node: node, OccurredAt: t0, Kind: secevent.SSHLoginFailed, User: "root",
		Remote:  &secevent.Remote{IP: netip.MustParseAddr("192.0.2.7"), Port: 22},
		Details: secevent.Details{Method: "password"},
	}
}

func TestKinds_AreTheNamesOfKonzept(t *testing.T) {
	want := []string{
		"ssh.login.failed", "ssh.login.invalid_user", "ssh.login.success", "ssh.preauth.disconnect",
		"sudo.auth.failed", "sudo.not_allowed", "sudo.command", "account.created", "account.deleted",
		"system.reboot", "system.oom_kill",
		// Written by the server itself (M2-24, T3, T4).
		"enrollment.token_used", "node.locked", "node.rotation_new_ip",
	}
	got := secevent.Kinds()
	if strings.Join(kindNames(got), ",") != strings.Join(want, ",") {
		t.Errorf("Kinds() = %v", got)
	}
	for _, name := range want {
		if k, err := secevent.ParseKind(name); err != nil || string(k) != name {
			t.Errorf("ParseKind(%q) = %v, %v", name, k, err)
		}
	}
	if _, err := secevent.ParseKind("ssh.login"); !errors.Is(err, secevent.ErrInvalid) {
		t.Error("unknown kind accepted")
	}
}

func kindNames(kinds []secevent.Kind) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}

func TestValidate(t *testing.T) {
	if err := failed().Validate(); err != nil {
		t.Fatal(err)
	}
	host := failed()
	host.Remote = &secevent.Remote{Host: "client.example.org"}
	if err := host.Validate(); err != nil {
		t.Errorf("host name: %v", err)
	}
	for name, change := range map[string]func(*secevent.Event){
		"no node":          func(e *secevent.Event) { e.Node = identity.NodeID{} },
		"no time":          func(e *secevent.Event) { e.OccurredAt = time.Time{} },
		"unknown kind":     func(e *secevent.Event) { e.Kind = "ssh.login" },
		"long user":        func(e *secevent.Event) { e.User = strings.Repeat("a", 129) },
		"ip and host":      func(e *secevent.Event) { e.Remote.Host = "x.example.org" },
		"bad host":         func(e *secevent.Event) { e.Remote = &secevent.Remote{Host: "<script>"} },
		"zone":             func(e *secevent.Event) { e.Remote.IP = netip.MustParseAddr("fe80::1%eth0") },
		"no address":       func(e *secevent.Event) { e.Remote = &secevent.Remote{Port: 22} },
		"long command":     func(e *secevent.Event) { e.Details.Command = strings.Repeat("x", 1025) },
		"long fingerprint": func(e *secevent.Event) { e.Details.KeyFingerprint = strings.Repeat("x", 129) },
		"invalid utf-8":    func(e *secevent.Event) { e.User = "a\xff" },
	} {
		e := failed()
		change(&e)
		if err := e.Validate(); !errors.Is(err, secevent.ErrInvalid) {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
}

// IPv4-mapped addresses are stored as IPv4, so that a filter for 192.0.2.0/24 finds them.
func TestValidate_UnmapsIPv4(t *testing.T) {
	e := failed()
	e.Remote.IP = netip.MustParseAddr("::ffff:192.0.2.7")
	e = e.Normalized()
	if e.Remote.IP != netip.MustParseAddr("192.0.2.7") {
		t.Errorf("ip = %v", e.Remote.IP)
	}
}
