package secevent_test

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
)

func TestFieldsOf_FollowTheTableOfKonzept(t *testing.T) {
	tests := map[secevent.Kind][]string{
		secevent.SSHLoginFailed:       {"ts", "user", "ip", "port", "method", "invalid_user"},
		secevent.SSHInvalidUser:       {"ts", "user", "ip", "port"},
		secevent.SSHLoginSucceeded:    {"ts", "user", "ip", "port", "method", "key_type", "key_fingerprint"},
		secevent.SSHPreauthDisconnect: {"ts", "user", "ip", "port"},
		secevent.SudoAuthFailed:       {"ts", "user", "tty"},
		secevent.SudoNotAllowed:       {"ts", "user", "target", "tty"},
		secevent.SudoCommand:          {"ts", "user", "target", "tty", "command"},
		secevent.AccountCreated:       {"ts", "user", "uid"},
		secevent.AccountDeleted:       {"ts", "user"},
		secevent.SystemReboot:         {"ts", "previous_boot_id", "uptime_before"},
		secevent.OOMKill:              {"ts", "pid", "process"},
		secevent.EnrollmentTokenUsed:  {"ts", "token", "ip"},
		secevent.NodeLocked:           {"ts", "reason"},
		secevent.NodeRotationNewIP:    {"ts", "ip", "previous_ip"},
	}
	for _, k := range secevent.Kinds() {
		want, ok := tests[k]
		if !ok {
			t.Errorf("%s: no expectation", k)
			continue
		}
		if got := secevent.FieldsOf(k); !slices.Equal(got, want) {
			t.Errorf("FieldsOf(%s) = %v, want %v", k, got, want)
		}
	}
	if got := secevent.FieldsOf("no.such.kind"); got != nil {
		t.Errorf("unknown kind has fields %v", got)
	}
}

func TestField_RendersTheValues(t *testing.T) {
	uid := uint32(1001)
	uptime := 3*time.Hour + 2*time.Second
	e := failed()
	e.Details.InvalidUser = true
	tests := []struct {
		event secevent.Event
		name  string
		want  string
	}{
		{e, "ts", "2026-11-20T12:00:00Z"},
		{e, "user", "root"},
		{e, "ip", "192.0.2.7"},
		{e, "port", "22"},
		{e, "method", "password"},
		{e, "invalid_user", "true"},
		{secevent.Event{Kind: secevent.SSHLoginFailed, Remote: &secevent.Remote{Host: "scanner.example"}}, "ip", "scanner.example"},
		{secevent.Event{Kind: secevent.AccountCreated, User: "bob", Details: secevent.Details{UID: &uid}}, "uid", "1001"},
		{secevent.Event{Kind: secevent.OOMKill, Details: secevent.Details{PID: 4711, Process: "java"}}, "pid", "4711"},
		{secevent.Event{Kind: secevent.OOMKill, Details: secevent.Details{PID: 4711, Process: "java"}}, "process", "java"},
		{secevent.Event{Kind: secevent.SystemReboot, Details: secevent.Details{PreviousBootID: "b1", UptimeBefore: &uptime}}, "uptime_before", "3h0m2s"},
		{secevent.Event{Kind: secevent.SudoNotAllowed, User: "alice", Details: secevent.Details{Target: "root", TTY: "pts/0"}}, "target", "root"},
	}
	for _, tt := range tests {
		got, ok := tt.event.Field(tt.name)
		if !ok || got != tt.want {
			t.Errorf("Field(%s) of %s = %q, %v; want %q", tt.name, tt.event.Kind, got, ok, tt.want)
		}
	}
}

// The server's own events (M2-24): token, lock reason and the previous address.
func TestField_RendersTheServerEvents(t *testing.T) {
	used := secevent.Event{
		Kind: secevent.EnrollmentTokenUsed, Details: secevent.Details{TokenID: "0193f5a0-0000-7000-8000-0000000000e1"},
		Remote: &secevent.Remote{IP: netip.MustParseAddr("192.0.2.7")},
	}
	locked := secevent.Event{Kind: secevent.NodeLocked, Details: secevent.Details{Reason: "second session from 192.0.2.66"}}
	moved := secevent.Event{
		Kind: secevent.NodeRotationNewIP, Remote: &secevent.Remote{IP: netip.MustParseAddr("192.0.2.8")},
		Details: secevent.Details{PreviousIP: "192.0.2.7"},
	}
	for _, tc := range []struct {
		e           secevent.Event
		field, want string
	}{
		{used, "token", "0193f5a0-0000-7000-8000-0000000000e1"},
		{used, "ip", "192.0.2.7"},
		{locked, "reason", "second session from 192.0.2.66"},
		{moved, "ip", "192.0.2.8"},
		{moved, "previous_ip", "192.0.2.7"},
	} {
		if got, ok := tc.e.Field(tc.field); !ok || got != tc.want {
			t.Errorf("%s.%s = %q, %v; want %q", tc.e.Kind, tc.field, got, ok, tc.want)
		}
	}
	long := secevent.Event{
		Node: identity.NodeID(uuid.New()), OccurredAt: time.Now(), Kind: secevent.NodeLocked,
		Details: secevent.Details{Reason: strings.Repeat("x", secevent.MaxReason+1)},
	}
	if err := long.Validate(); !errors.Is(err, secevent.ErrInvalid) {
		t.Errorf("a reason over %d bytes: %v", secevent.MaxReason, err)
	}
}

func TestField_IsMissingWhenEmptyOrNotOfTheKind(t *testing.T) {
	e := failed()
	e.Remote.Port = 0
	for _, name := range []string{"port", "invalid_user", "uid", "process", "nonsense"} {
		if got, ok := e.Field(name); ok {
			t.Errorf("Field(%s) = %q, want none", name, got)
		}
	}
	// An empty user name is what an attacker tried: it is a value.
	e.User = ""
	if got, ok := e.Field("user"); !ok || got != "" {
		t.Errorf("empty user = %q, %v", got, ok)
	}
	if _, ok := (secevent.Event{Kind: secevent.SSHPreauthDisconnect}).Field("ip"); ok {
		t.Error("ip without remote")
	}
}

func TestGroupKey_GroupsIPv6ByPrefix(t *testing.T) {
	tests := []struct {
		remote *secevent.Remote
		want   string
	}{
		{&secevent.Remote{IP: netip.MustParseAddr("192.0.2.7")}, "192.0.2.7"},
		// Attackers get a /64 and more: addresses within one count as one source.
		{&secevent.Remote{IP: netip.MustParseAddr("2001:db8:1:2:aaaa::1")}, "2001:db8:1:2::/64"},
		{&secevent.Remote{IP: netip.MustParseAddr("2001:db8:1:2:bbbb::9")}, "2001:db8:1:2::/64"},
		{&secevent.Remote{Host: "scanner.example"}, "scanner.example"},
	}
	for _, tt := range tests {
		e := secevent.Event{Kind: secevent.SSHLoginFailed, Remote: tt.remote}
		if got, ok := e.GroupKey("ip"); !ok || got != tt.want {
			t.Errorf("GroupKey(ip) of %+v = %q, %v; want %q", tt.remote, got, ok, tt.want)
		}
	}
	e := secevent.Event{Kind: secevent.SudoAuthFailed, User: "alice"}
	if got, ok := e.GroupKey("user"); !ok || got != "alice" {
		t.Errorf("GroupKey(user) = %q, %v", got, ok)
	}
	if _, ok := e.GroupKey("ip"); ok {
		t.Error("sudo event grouped by ip")
	}
}

func TestGroupable_AreIPAndUserWhereTheKindHasThem(t *testing.T) {
	if !secevent.Groupable(secevent.SSHLoginFailed, "ip") || !secevent.Groupable(secevent.SSHLoginFailed, "user") {
		t.Error("ssh.login.failed groups by ip and user")
	}
	if secevent.Groupable(secevent.SudoAuthFailed, "ip") || secevent.Groupable(secevent.SystemReboot, "user") {
		t.Error("grouped by a field the kind lacks")
	}
	if secevent.Groupable(secevent.SSHLoginFailed, "port") || secevent.Groupable(secevent.SSHLoginFailed, "ts") {
		t.Error("only ip and user group")
	}
}
