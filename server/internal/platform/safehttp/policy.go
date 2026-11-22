// Package safehttp is the HTTP client for every request the server makes to targets that users
// configure (webhooks, chat and push channels; KONZEPT §18.6, M2-09). It refuses internal and
// special addresses unless the operator allowlists them, pins the address it checked
// (DNS rebinding), checks every redirect again, and bounds time and response size.
package safehttp

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// ErrBlocked means the target address is internal or special and not on the allowlist.
var ErrBlocked = errors.New("target address not allowed")

// ErrInvalidAllowlist means an allowlist entry is malformed or too broad.
var ErrInvalidAllowlist = errors.New("invalid outbound allowlist")

// blocked are the address ranges no request may reach without the allowlist, by class: the
// IANA special-purpose registries without the documentation ranges, which are routed nowhere.
var blocked = []struct {
	prefix netip.Prefix
	class  string
}{
	{netip.MustParsePrefix("0.0.0.0/32"), "unspecified"},
	{netip.MustParsePrefix("0.0.0.0/8"), "this network"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("10.0.0.0/8"), "private"},
	{netip.MustParsePrefix("172.16.0.0/12"), "private"},
	{netip.MustParsePrefix("192.168.0.0/16"), "private"},
	{netip.MustParsePrefix("100.64.0.0/10"), "shared (carrier-grade NAT)"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local"},
	{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignments"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},
	{netip.MustParsePrefix("::/128"), "unspecified"},
	{netip.MustParsePrefix("::1/128"), "loopback"},
	{netip.MustParsePrefix("::/96"), "deprecated IPv4-compatible"},
	{netip.MustParsePrefix("64:ff9b:1::/48"), "private (local-use NAT64)"},
	{netip.MustParsePrefix("100::/64"), "discard"},
	{netip.MustParsePrefix("2001::/32"), "teredo"},
	{netip.MustParsePrefix("2002::/16"), "6to4"},
	{netip.MustParsePrefix("fc00::/7"), "unique local"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("fec0::/10"), "site-local"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
}

// nat64 embeds an IPv4 address in its last 32 bits (RFC 6052); the IPv4 address is checked.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// embedding are the IPv6 ranges that carry an IPv4 address (NAT64, IPv4-compatible, 6to4,
// Teredo): allowlisting them would open IPv4 targets – the metadata service, loopback – in
// disguise (M2-28). The allowlist names the IPv4 networks instead.
var embedding = []netip.Prefix{
	nat64,
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

// ipv6Loopback lies in ::/96 but embeds nothing: a local ntfy over ::1 may be allowlisted.
var ipv6Loopback = netip.MustParsePrefix("::1/128")

// metadata are the cloud metadata services, which hand out credentials: no allowlist covers them.
var metadata = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"), // AWS, GCP, Azure, OpenStack, …
	netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6
	netip.MustParseAddr("100.100.100.200"), // Alibaba
	netip.MustParseAddr("192.0.0.192"),     // Oracle
}

// Policy decides which addresses requests may reach.
type Policy struct {
	allow []netip.Prefix
}

// NewPolicy returns the policy with the operator's allowlist of internal targets. An entry must
// not open everything, be an IPv4-mapped prefix (use the IPv4 form), overlap a range embedding
// IPv4 addresses or cover a metadata service.
func NewPolicy(allow []netip.Prefix) (Policy, error) {
	for _, p := range allow {
		switch {
		case !p.IsValid() || p != p.Masked():
			return Policy{}, fmt.Errorf("%w: %v is no network address with prefix length", ErrInvalidAllowlist, p)
		case p.Bits() == 0:
			return Policy{}, fmt.Errorf("%w: %v allows every address", ErrInvalidAllowlist, p)
		case p.Addr().Is4In6():
			return Policy{}, fmt.Errorf("%w: write %v in its IPv4 form", ErrInvalidAllowlist, p)
		}
		for _, e := range embedding {
			if p.Overlaps(e) && p != ipv6Loopback {
				return Policy{}, fmt.Errorf("%w: %v overlaps %v, which embeds IPv4 addresses; allow the IPv4 networks", ErrInvalidAllowlist, p, e)
			}
		}
		for _, m := range metadata {
			if p.Contains(m) {
				return Policy{}, fmt.Errorf("%w: %v covers the metadata service %v", ErrInvalidAllowlist, p, m)
			}
		}
	}
	return Policy{allow: append([]netip.Prefix(nil), allow...)}, nil
}

// ParseAllowlist reads a comma-separated list of networks (10.0.0.0/8) and addresses
// (192.168.5.7) and checks it like NewPolicy.
func ParseAllowlist(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		var p netip.Prefix
		if strings.Contains(item, "/") {
			var err error
			if p, err = netip.ParsePrefix(item); err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrInvalidAllowlist, item, err)
			}
		} else {
			addr, err := netip.ParseAddr(item)
			if err != nil {
				return nil, fmt.Errorf("%w: %q is no address or network", ErrInvalidAllowlist, item)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, p)
	}
	if _, err := NewPolicy(out); err != nil {
		return nil, err
	}
	return out, nil
}

// Allowlisted reports whether the address is on the allowlist.
func (p Policy) Allowlisted(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, a := range p.allow {
		if a.Contains(ip) {
			return true
		}
	}
	return false
}

// Check returns nil if a request may reach the address, else ErrBlocked with its class.
func (p Policy) Check(ip netip.Addr) error {
	if !ip.IsValid() {
		return fmt.Errorf("%w: no address", ErrBlocked)
	}
	ip = ip.Unmap().WithZone("")
	// The IPv4 form decides, before the allowlist (M2-28).
	if nat64.Contains(ip) {
		b := ip.As16()
		return p.Check(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	if p.Allowlisted(ip) {
		return nil
	}
	for _, b := range blocked {
		if b.prefix.Contains(ip) {
			return fmt.Errorf("%w: %s address", ErrBlocked, b.class)
		}
	}
	return nil
}
