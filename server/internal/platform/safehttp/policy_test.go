package safehttp_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func prefixes(t *testing.T, list ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func policy(t *testing.T, allow ...string) safehttp.Policy {
	t.Helper()
	p, err := safehttp.NewPolicy(prefixes(t, allow...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Every class of internal or special address is blocked, in IPv4, IPv6 and the IPv6 forms that
// embed an IPv4 address.
func TestPolicy_BlocksInternalAddressClasses(t *testing.T) {
	p := policy(t)
	for addr, class := range map[string]string{
		"0.0.0.0":         "unspecified",
		"0.1.2.3":         "this network",
		"127.0.0.1":       "loopback",
		"127.255.255.254": "loopback",
		"10.1.2.3":        "private",
		"172.16.0.1":      "private",
		"172.31.255.255":  "private",
		"192.168.1.1":     "private",
		"100.64.0.1":      "shared",
		"100.100.100.200": "shared", // Alibaba metadata
		"169.254.169.254": "link-local",
		"169.254.0.1":     "link-local",
		"192.0.0.192":     "protocol assignments", // Oracle metadata
		"198.18.0.1":      "benchmarking",
		"224.0.0.1":       "multicast",
		"240.0.0.1":       "reserved",
		"255.255.255.255": "reserved",
		"::":              "unspecified",
		"::1":             "loopback",
		"fc00::1":         "unique local",
		"fd00:ec2::254":   "unique local", // AWS metadata over IPv6
		"fe80::1":         "link-local",
		"fec0::1":         "site-local",
		"ff02::1":         "multicast",
		"100::1":          "discard",
		"2001::1":         "teredo",
		"2002:a00:1::1":   "6to4",
		// IPv4 inside IPv6.
		"::ffff:127.0.0.1":       "loopback",
		"::ffff:169.254.169.254": "link-local",
		"::ffff:10.0.0.1":        "private",
		"64:ff9b::a00:1":         "private", // NAT64 of 10.0.0.1
		"64:ff9b::7f00:1":        "loopback",
		"64:ff9b:1::a00:1":       "private", // local-use NAT64
		"::127.0.0.1":            "deprecated",
	} {
		err := p.Check(netip.MustParseAddr(addr))
		if !errors.Is(err, safehttp.ErrBlocked) {
			t.Errorf("%s: Check() = %v, want blocked", addr, err)
			continue
		}
		if !strings.Contains(err.Error(), class) {
			t.Errorf("%s: %q does not name the class %q", addr, err, class)
		}
	}
	if err := p.Check(netip.Addr{}); !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("invalid address: %v", err)
	}
}

// The documentation ranges are not routed anywhere; they stand in for public addresses here.
func TestPolicy_AllowsOtherAddresses(t *testing.T) {
	p := policy(t)
	for _, addr := range []string{"192.0.2.10", "198.51.100.7", "203.0.113.200", "2001:db8::1", "::ffff:192.0.2.10", "64:ff9b::c000:20a", "172.32.0.1", "100.128.0.1"} {
		if err := p.Check(netip.MustParseAddr(addr)); err != nil {
			t.Errorf("%s: Check() = %v, want allowed", addr, err)
		}
	}
}

func TestPolicy_AllowlistOpensInternalTargets(t *testing.T) {
	p := policy(t, "10.20.0.0/16", "fd12:3456::/32")
	for _, addr := range []string{"10.20.1.2", "::ffff:10.20.1.2", "fd12:3456::9"} {
		ip := netip.MustParseAddr(addr)
		if err := p.Check(ip); err != nil || !p.Allowlisted(ip) {
			t.Errorf("%s: Check() = %v, allowlisted %v; want allowed by the list", addr, err, p.Allowlisted(ip))
		}
	}
	for _, addr := range []string{"10.21.0.1", "127.0.0.1", "169.254.169.254"} {
		if err := p.Check(netip.MustParseAddr(addr)); !errors.Is(err, safehttp.ErrBlocked) {
			t.Errorf("%s: Check() = %v, want blocked", addr, err)
		}
	}
	if p.Allowlisted(netip.MustParseAddr("192.0.2.10")) {
		t.Error("a public address is not on the list")
	}
}

// The metadata services hand out cloud credentials: no allowlist entry may cover them, nor may
// one open everything.
func TestNewPolicy_RejectsDangerousAllowlists(t *testing.T) {
	for _, list := range [][]string{
		{"0.0.0.0/0"},
		{"::/0"},
		{"169.254.0.0/16"},
		{"169.254.169.254/32"},
		{"fd00::/8"},
		{"100.64.0.0/10"},
		{"192.0.0.0/24"},
		{"::ffff:0:0/96"},
	} {
		if _, err := safehttp.NewPolicy(prefixes(t, list...)); err == nil {
			t.Errorf("allowlist %v accepted", list)
		}
	}
	if _, err := safehttp.NewPolicy(prefixes(t, "0.0.0.0/0")); err == nil || !strings.Contains(err.Error(), "every address") {
		t.Errorf("0.0.0.0/0: %v, want it named as opening everything", err)
	}
	if _, err := safehttp.NewPolicy(prefixes(t, "127.0.0.1/32", "10.0.0.0/8", "fd12::/16")); err != nil {
		t.Errorf("usual allowlist rejected: %v", err)
	}
}

func TestParseAllowlist(t *testing.T) {
	got, err := safehttp.ParseAllowlist(" 10.0.0.0/8, 192.168.5.7 ,fd12::/16")
	if err != nil || len(got) != 3 || got[1] != netip.MustParsePrefix("192.168.5.7/32") || got[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Errorf("ParseAllowlist() = %v, %v", got, err)
	}
	if got, err := safehttp.ParseAllowlist(""); err != nil || len(got) != 0 {
		t.Errorf("empty: %v, %v", got, err)
	}
	for _, bad := range []string{"10.0.0.1/8", "intranet.example", "10.0.0.0/33", "169.254.169.254"} {
		if _, err := safehttp.ParseAllowlist(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
