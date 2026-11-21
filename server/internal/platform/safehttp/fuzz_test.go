package safehttp_test

import (
	"net/netip"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// Accepted URLs are http(s) with a host and no credentials, and an address literal in them is
// one the policy allows. Nothing panics.
func FuzzValidateURL(f *testing.F) {
	for _, seed := range []string{
		"https://hooks.example/services/x", "http://[::ffff:127.0.0.1]:80/", "https://0x7f.1/", "https://127.1/",
		"https://user@example.com/", "https://[fe80::1%25eth0]/", "https://example.com:0/", "HTTPS://EXAMPLE.COM/",
		"https://2130706433/", "https://[64:ff9b::a00:1]/", "http://example.com@10.0.0.1/", "https://example.com\\@10.0.0.1/",
	} {
		f.Add(seed)
	}
	var p safehttp.Policy
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := p.ValidateURL(raw)
		if err != nil {
			return
		}
		if (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Hostname() == "" {
			t.Fatalf("accepted %q as %+v", raw, u)
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && p.Check(ip) != nil {
			t.Fatalf("accepted the blocked literal %s in %q", ip, raw)
		}
	})
}

// An IPv4 address and its IPv4-mapped and NAT64 forms get the same verdict.
func FuzzCheck_SameVerdictForEmbeddedIPv4(f *testing.F) {
	f.Add([]byte{127, 0, 0, 1})
	f.Add([]byte{169, 254, 169, 254})
	f.Add([]byte{192, 0, 2, 10})
	p, err := safehttp.NewPolicy([]netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) != 4 {
			return
		}
		v4 := netip.AddrFrom4([4]byte(b))
		mapped := netip.AddrFrom16(v4.As16())
		n := [16]byte{0, 0x64, 0xff, 0x9b, 12: b[0], 13: b[1], 14: b[2], 15: b[3]}
		nat64 := netip.AddrFrom16(n)
		want := p.Check(v4) == nil
		if (p.Check(mapped) == nil) != want || (p.Check(nat64) == nil) != want {
			t.Fatalf("%v: v4 %v, mapped %v, nat64 %v", v4, p.Check(v4), p.Check(mapped), p.Check(nat64))
		}
	})
}
