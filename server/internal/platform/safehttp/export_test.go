package safehttp

import (
	"crypto/x509"
	"syscall"
)

// UseFakeNet replaces name resolution, dialing and the trusted roots (tests only). The Control
// check of the socket is not part of the fake dial; TestControl covers it.
func UseFakeNet(c *Client, r resolver, dial dialFunc, roots *x509.CertPool) {
	c.resolver = r
	c.dial = dial
	c.transport.TLSClientConfig.RootCAs = roots
}

// Control returns the socket check of a policy.
func Control(p Policy) func(network, address string, _ syscall.RawConn) error {
	return control(p)
}

// HasProxy reports whether the transport sends through a proxy.
func HasProxy(c *Client) bool { return c.transport.Proxy != nil }
