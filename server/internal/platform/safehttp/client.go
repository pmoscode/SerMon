package safehttp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Errors of a request besides ErrBlocked.
var (
	ErrInvalidURL       = errors.New("invalid target URL")
	ErrInsecureScheme   = errors.New("plain http only to allowlisted targets")
	ErrTooManyRedirects = errors.New("too many redirects")
	// ErrRedirectHost: redirects stay on the host, so the caller's headers (keys) don't travel.
	ErrRedirectHost     = errors.New("redirect to another host")
	ErrResponseTooLarge = errors.New("response too large")
)

// Defaults of Options.
const (
	DefaultTimeout          = 15 * time.Second
	DefaultMaxResponseBytes = 1 << 20
	DefaultMaxRedirects     = 3
	DefaultUserAgent        = "SerMon"

	maxURLBytes           = 2048
	dialTimeout           = 5 * time.Second
	tlsHandshakeTimeout   = 5 * time.Second
	responseHeaderTimeout = 10 * time.Second
	maxHeaderBytes        = 64 << 10
)

// Options configure a client; zero values take the defaults.
type Options struct {
	// Policy holds the operator's allowlist of internal targets.
	Policy Policy
	// Timeout bounds a request with its redirects, until the body is closed.
	Timeout time.Duration
	// MaxResponseBytes bounds the response body.
	MaxResponseBytes int64
	// MaxRedirects bounds the redirects that are followed.
	MaxRedirects int
	// UserAgent is sent unless a request sets one.
	UserAgent string
}

type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Client sends requests to user-configured targets. It is safe for concurrent use.
type Client struct {
	policy    Policy
	o         Options
	resolver  resolver
	dial      dialFunc
	transport *http.Transport
	http      *http.Client
}

// New returns a client. Proxy variables of the environment are ignored: a proxy would reach
// what the checks refuse.
func New(o Options) *Client {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = DefaultMaxRedirects
	}
	if o.UserAgent == "" {
		o.UserAgent = DefaultUserAgent
	}
	c := &Client{policy: o.Policy, o: o, resolver: net.DefaultResolver}
	// The socket checks the address again, whatever dials it.
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second, Control: control(o.Policy)}
	c.dial = dialer.DialContext
	c.transport = &http.Transport{
		Proxy:                  nil,
		DialContext:            c.dialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maxHeaderBytes,
		IdleConnTimeout:        30 * time.Second,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    2,
	}
	c.http = &http.Client{Transport: roundTripper{c: c}, CheckRedirect: c.checkRedirect}
	return c
}

// ValidateURL checks a target URL before it is stored (ValidateURL of the client's policy).
func (c *Client) ValidateURL(raw string) (*url.URL, error) {
	return c.policy.ValidateURL(raw)
}

// Do sends the request. Errors name method and host only: webhook URLs carry secrets in their
// path and query. The response body must be closed.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), c.o.Timeout)
	req = req.WithContext(ctx)
	if req.Header.Get("User-Agent") == "" {
		req.Header = req.Header.Clone()
		if req.Header == nil {
			req.Header = http.Header{}
		}
		req.Header.Set("User-Agent", c.o.UserAgent)
	}
	resp, err := c.http.Do(req) //nolint:gosec // G704: this is the SSRF guard – every dial and redirect is checked
	if err != nil {
		cancel()
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Hostname(), err)
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type plainKey struct{}

// dialContext resolves the host once, checks every address and dials the checked ones only.
func (c *Client) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = c.resolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve: no address for %s", host)
	}
	plain := ctx.Value(plainKey{}) != nil
	// One bad answer refuses the name: an attacker controls its records.
	for _, ip := range ips {
		if err := c.policy.Check(ip); err != nil {
			return nil, err
		}
		if plain && !c.policy.Allowlisted(ip) {
			return nil, ErrInsecureScheme
		}
	}
	var errs []error
	for _, ip := range ips {
		conn, err := c.dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// control is the socket's check before it connects.
func control(p Policy) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: unparsable socket address", ErrBlocked)
		}
		return p.Check(ap.Addr())
	}
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > c.o.MaxRedirects {
		return ErrTooManyRedirects
	}
	if _, err := c.policy.checkURL(req.URL); err != nil {
		return err
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return ErrRedirectHost
	}
	if req.URL.Scheme == "http" && via[len(via)-1].URL.Scheme == "https" {
		return ErrInsecureScheme
	}
	return nil
}

// roundTripper checks the URL of every request (redirects included), marks plain HTTP for the
// dial and bounds the response body.
type roundTripper struct{ c *Client }

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := rt.c.policy.checkURL(req.URL); err != nil {
		return nil, err
	}
	if req.URL.Scheme == "http" {
		req = req.WithContext(context.WithValue(req.Context(), plainKey{}, true))
	}
	resp, err := rt.c.transport.RoundTrip(req)
	if err != nil {
		return nil, err //nolint:wrapcheck // Do adds method and host
	}
	if resp.ContentLength > rt.c.o.MaxResponseBytes {
		_ = resp.Body.Close()
		return nil, ErrResponseTooLarge
	}
	resp.Body = &limitedBody{ReadCloser: resp.Body, left: rt.c.o.MaxResponseBytes}
	return resp, nil
}

// limitedBody fails with ErrResponseTooLarge once the body goes beyond the limit.
type limitedBody struct {
	io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var one [1]byte
		n, err := b.ReadCloser.Read(one[:])
		if n > 0 || err == nil {
			return 0, ErrResponseTooLarge
		}
		return 0, err //nolint:wrapcheck // io.EOF must pass unchanged
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err //nolint:wrapcheck // io.EOF must pass unchanged
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close() //nolint:wrapcheck // the body's own error
}

// ValidateURL checks a target URL before it is stored: http or https, a host, no credentials in
// the URL, a valid port, at most 2 KiB, and an address literal the policy allows. Names are
// checked when a request resolves them.
func (p Policy) ValidateURL(raw string) (*url.URL, error) {
	if len(raw) > maxURLBytes {
		return nil, fmt.Errorf("%w: longer than %d bytes", ErrInvalidURL, maxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, errors.Unwrap(err))
	}
	return p.checkURL(u)
}

func (p Policy) checkURL(u *url.URL) (*url.URL, error) {
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return nil, fmt.Errorf("%w: scheme must be https or http", ErrInvalidURL)
	case u.User != nil:
		return nil, fmt.Errorf("%w: no credentials in the URL", ErrInvalidURL)
	case u.Opaque != "" || u.Hostname() == "":
		return nil, fmt.Errorf("%w: no host", ErrInvalidURL)
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%w: port must be 1 to 65535", ErrInvalidURL)
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if err := p.Check(ip); err != nil {
			return nil, err
		}
	}
	return u, nil
}
