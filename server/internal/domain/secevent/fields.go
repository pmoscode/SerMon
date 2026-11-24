package secevent

import (
	"net/netip"
	"slices"
	"strconv"
	"time"
)

// Fields of the events by name, as alert rules use them (KONZEPT §11, M2-08): notifyFields copy
// them into the notification, groupBy splits the alerts by ip or user.
var fieldsOf = map[Kind][]string{
	SSHLoginFailed:       {"ts", "user", "ip", "port", "method", "invalid_user"},
	SSHInvalidUser:       {"ts", "user", "ip", "port"},
	SSHLoginSucceeded:    {"ts", "user", "ip", "port", "method", "key_type", "key_fingerprint", "new_source"},
	SSHPreauthDisconnect: {"ts", "user", "ip", "port"},
	SudoAuthFailed:       {"ts", "user", "tty"},
	SudoNotAllowed:       {"ts", "user", "target", "tty"},
	SudoCommand:          {"ts", "user", "target", "tty", "command"},
	AccountCreated:       {"ts", "user", "uid"},
	AccountDeleted:       {"ts", "user"},
	SystemReboot:         {"ts", "previous_boot_id", "uptime_before"},
	OOMKill:              {"ts", "pid", "process"},
	EnrollmentTokenUsed:  {"ts", "token", "ip"},
	NodeLocked:           {"ts", "reason"},
	NodeRotationNewIP:    {"ts", "ip", "previous_ip"},
}

// FieldSource is the alert field set when a counted event is unverified (ADR 0025); it is no
// field of the events and cannot be chosen in notifyFields or groupBy.
const (
	FieldSource      = "source"
	SourceUnverified = "unverified"
)

// FieldsOf returns the field names of a kind, nil for an unknown kind.
func FieldsOf(k Kind) []string {
	return slices.Clone(fieldsOf[k])
}

// Groupable reports whether alerts on the kind can be split by the field: ip and user, where the
// kind has them.
func Groupable(k Kind, name string) bool {
	return (name == "ip" || name == "user") && slices.Contains(fieldsOf[k], name)
}

// Field returns a field of the event as text; false if the kind has no such field or the event
// leaves it empty. ip is the address or, without one, the host name. An empty user name is a
// value (what an attacker tried).
func (e Event) Field(name string) (string, bool) {
	if !slices.Contains(fieldsOf[e.Kind], name) {
		return "", false
	}
	d := e.Details
	var v string
	switch name {
	case "ts":
		v = e.OccurredAt.UTC().Format(time.RFC3339)
	case "user":
		return e.User, true
	case "ip":
		if e.Remote == nil {
			return "", false
		}
		v = e.Remote.Host
		if e.Remote.IP.IsValid() {
			v = e.Remote.IP.String()
		}
	case "port":
		if e.Remote != nil && e.Remote.Port != 0 {
			v = strconv.Itoa(int(e.Remote.Port))
		}
	case "method":
		v = d.Method
	case "invalid_user":
		if d.InvalidUser {
			v = "true"
		}
	case "key_type":
		v = d.KeyType
	case "key_fingerprint":
		v = d.KeyFingerprint
	case "tty":
		v = d.TTY
	case "target":
		v = d.Target
	case "command":
		v = d.Command
	case "uid":
		if d.UID != nil {
			v = strconv.FormatUint(uint64(*d.UID), 10)
		}
	case "pid":
		if d.PID != 0 {
			v = strconv.FormatUint(uint64(d.PID), 10)
		}
	case "process":
		v = d.Process
	case "previous_boot_id":
		v = d.PreviousBootID
	case "uptime_before":
		if d.UptimeBefore != nil {
			v = d.UptimeBefore.String()
		}
	case "token":
		v = d.TokenID
	case "reason":
		v = d.Reason
	case "previous_ip":
		v = d.PreviousIP
	case "new_source":
		if e.NewSource {
			v = "true"
		}
	}
	return v, v != ""
}

// ipv6Group is the prefix that counts as one source: providers hand out a /64 at least, and an
// attacker rotating addresses within it must not slip under a threshold per address.
const ipv6Group = 64

// GroupKey returns the value alerts are split by (Groupable fields): ip groups IPv6 addresses by
// their /64, user is the user name.
func (e Event) GroupKey(name string) (string, bool) {
	if !Groupable(e.Kind, name) {
		return "", false
	}
	if name == "ip" && e.Remote != nil && e.Remote.IP.Is6() && !e.Remote.IP.Is4In6() {
		return netip.PrefixFrom(e.Remote.IP, ipv6Group).Masked().String(), true
	}
	return e.Field(name)
}

// LoginSource returns what a successful login is known by on its node (M3-16): the address, an
// IPv6 address by its /64 like GroupKey, else the host name sshd logged; false for other kinds
// and logins without either (the console).
func (e Event) LoginSource() (string, bool) {
	if e.Kind != SSHLoginSucceeded || e.Remote == nil || (!e.Remote.IP.IsValid() && e.Remote.Host == "") {
		return "", false
	}
	return e.GroupKey("ip")
}
