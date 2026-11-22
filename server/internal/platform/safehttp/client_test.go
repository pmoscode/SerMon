package safehttp_test

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// fakeNet resolves names from a table (a name may answer differently on each lookup) and routes
// every dialed address to a local test server, recording what was dialed.
type fakeNet struct {
	mu      sync.Mutex
	names   map[string][][]netip.Addr // answers per lookup; the last repeats
	lookups map[string]int
	routes  map[netip.Addr]string // checked address → listener of a test server
	dialed  []string
}

func newFakeNet() *fakeNet {
	return &fakeNet{names: map[string][][]netip.Addr{}, lookups: map[string]int{}, routes: map[netip.Addr]string{}}
}

func (f *fakeNet) name(host string, answers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var addrs []netip.Addr
	for _, a := range answers {
		addrs = append(addrs, netip.MustParseAddr(a))
	}
	f.names[host] = append(f.names[host], addrs)
}

func (f *fakeNet) route(ip string, srv *httptest.Server) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[netip.MustParseAddr(ip)] = srv.Listener.Addr().String()
}

func (f *fakeNet) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	answers, ok := f.names[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	n := f.lookups[host]
	f.lookups[host]++
	return answers[min(n, len(answers)-1)], nil
}

func (f *fakeNet) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.dialed = append(f.dialed, addr)
	ap, err := netip.ParseAddrPort(addr)
	target, ok := f.routes[ap.Addr()]
	f.mu.Unlock()
	if err != nil || !ok {
		return nil, errors.New("fake net: no route to " + addr)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", target)
}

func (f *fakeNet) dials() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dialed...)
}

type setup struct {
	net    *fakeNet
	client *safehttp.Client
}

// newClient returns a client over the fake net that trusts the TLS test servers.
func newClient(t *testing.T, o safehttp.Options, servers ...*httptest.Server) setup {
	t.Helper()
	fn := newFakeNet()
	c := safehttp.New(o)
	roots := x509.NewCertPool()
	for _, s := range servers {
		if s.Certificate() != nil {
			roots.AddCert(s.Certificate())
		}
	}
	safehttp.UseFakeNet(c, fn, fn.dial, roots)
	return setup{net: fn, client: c}
}

func get(t *testing.T, c *safehttp.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

func ok(t *testing.T, c *safehttp.Client, url string) string {
	t.Helper()
	resp, err := get(t, c, url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func hello(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") }

// httptest's certificate is valid for example.com.
func TestClient_ReachesAPublicTargetOverTheCheckedAddress(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(hello))
	defer srv.Close()
	s := newClient(t, safehttp.Options{}, srv)
	s.net.name("example.com", "192.0.2.10")
	s.net.route("192.0.2.10", srv)
	if got := ok(t, s.client, "https://example.com/hook"); got != "hello" {
		t.Errorf("body = %q", got)
	}
	if d := s.net.dials(); len(d) != 1 || d[0] != "192.0.2.10:443" {
		t.Errorf("dialed %v, want the checked address", d)
	}
}

func TestClient_BlocksInternalTargets(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(hello))
	defer srv.Close()
	s := newClient(t, safehttp.Options{}, srv)
	s.net.name("intranet.example", "10.0.0.1")
	s.net.name("mapped.example", "::ffff:127.0.0.1")
	s.net.name("metadata.example", "169.254.169.254")
	s.net.name("mixed.example", "192.0.2.10", "10.0.0.1")
	s.net.route("192.0.2.10", srv)
	for _, url := range []string{
		"https://intranet.example/", "https://mapped.example/", "https://metadata.example/latest/meta-data/",
		"https://mixed.example/", "https://127.0.0.1/", "https://[::1]/", "https://[::ffff:169.254.169.254]/",
		"https://[fe80::1%25eth0]/", "https://localhost/",
	} {
		resp, err := get(t, s.client, url)
		if err == nil {
			resp.Body.Close()
		}
		// localhost is unknown to the fake resolver; a real one answers with loopback.
		if !errors.Is(err, safehttp.ErrBlocked) && (url != "https://localhost/" || err == nil) {
			t.Errorf("GET %s = %v, want blocked", url, err)
		}
	}
	if d := s.net.dials(); len(d) != 0 {
		t.Errorf("dialed %v, want nothing", d)
	}
}

// DNS rebinding: the name answers with a public address first and an internal one later. Each
// connection resolves once and dials what it checked; the internal answer is refused.
func TestClient_DNSAnswerChangingBetweenRequests(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close") // no reuse: the next request dials again
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()
	s := newClient(t, safehttp.Options{}, srv)
	s.net.name("example.com", "192.0.2.10")
	s.net.name("example.com", "127.0.0.1")
	s.net.route("192.0.2.10", srv)
	s.net.route("127.0.0.1", srv)
	ok(t, s.client, "https://example.com/")
	resp, err := get(t, s.client, "https://example.com/")
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, safehttp.ErrBlocked) {
		t.Errorf("second request = %v, want blocked", err)
	}
	if d := s.net.dials(); len(d) != 1 || d[0] != "192.0.2.10:443" {
		t.Errorf("dialed %v, want the first, checked address only", d)
	}
}

func TestClient_RedirectsAreCheckedAgain(t *testing.T) {
	var target string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			hello(w, r)
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()
	s := newClient(t, safehttp.Options{}, srv)
	s.net.name("example.com", "192.0.2.10")
	s.net.name("metadata.example", "169.254.169.254")
	s.net.route("192.0.2.10", srv)
	for _, to := range []string{"https://metadata.example/latest/", "https://169.254.169.254/latest/", "http://10.0.0.1/"} {
		target = to
		resp, err := get(t, s.client, "https://example.com/start")
		if err == nil {
			resp.Body.Close()
		}
		// Addresses are blocked as such; a name is refused as another host before it is resolved.
		if !errors.Is(err, safehttp.ErrBlocked) && !errors.Is(err, safehttp.ErrRedirectHost) {
			t.Errorf("redirect to %s = %v, want blocked", to, err)
		}
	}
	// No downgrade to plain HTTP, not even to a public address.
	target = "http://example.com/final"
	resp, err := get(t, s.client, "https://example.com/start")
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, safehttp.ErrInsecureScheme) {
		t.Errorf("redirect to http = %v, want insecure", err)
	}
	target = "https://example.com/final"
	if got := ok(t, s.client, "https://example.com/start"); got != "hello" {
		t.Errorf("allowed redirect: %q", got)
	}
}

// A redirect to another host would carry the notifier's own headers along (Go drops only
// Authorization and cookies) – a Gotify key, for instance (M2-28). Only the same host follows.
func TestClient_RedirectsStayOnTheHost(t *testing.T) {
	var target string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			hello(w, r)
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()
	s := newClient(t, safehttp.Options{}, srv)
	s.net.name("example.com", "192.0.2.10")
	s.net.name("other.example", "192.0.2.10")
	s.net.route("192.0.2.10", srv)
	target = "https://other.example/final"
	resp, err := get(t, s.client, "https://example.com/start")
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, safehttp.ErrRedirectHost) {
		t.Errorf("redirect to another host = %v, want refused", err)
	}
	target = "https://example.com/final"
	if got := ok(t, s.client, "https://example.com/start"); got != "hello" {
		t.Errorf("redirect on the same host: %q", got)
	}
}

func TestClient_LimitsRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) > 5 { // /a, /aa, …, /aaaaa

			hello(w, r)
			return
		}
		http.Redirect(w, r, r.URL.Path+"a", http.StatusFound)
	}))
	defer srv.Close()
	s := newClient(t, safehttp.Options{}, srv)
	s.net.name("example.com", "192.0.2.10")
	s.net.route("192.0.2.10", srv)
	if got := ok(t, s.client, "https://example.com/aa"); got != "hello" { // /aaa, /aaaa, /aaaaa: three
		t.Errorf("three redirects: %q", got)
	}
	resp, err := get(t, s.client, "https://example.com/a") // four
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, safehttp.ErrTooManyRedirects) {
		t.Errorf("four redirects = %v", err)
	}
}

// Plain HTTP only to allowlisted internal targets (an internal ntfy); public ones need TLS.
func TestClient_PlainHTTPOnlyToTheAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(hello))
	defer srv.Close()
	p, err := safehttp.NewPolicy(prefixes(t, "10.9.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	s := newClient(t, safehttp.Options{Policy: p})
	s.net.name("ntfy.internal.example", "10.9.0.5")
	s.net.name("example.com", "192.0.2.10")
	s.net.route("10.9.0.5", srv)
	s.net.route("192.0.2.10", srv)
	if got := ok(t, s.client, "http://ntfy.internal.example/alerts"); got != "hello" {
		t.Errorf("allowlisted: %q", got)
	}
	resp, err := get(t, s.client, "http://example.com/hook")
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, safehttp.ErrInsecureScheme) {
		t.Errorf("public over http = %v, want insecure", err)
	}
}

func TestClient_LimitsTheResponseSize(t *testing.T) {
	big := strings.Repeat("x", 2048)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			w.(http.Flusher).Flush() //nolint:forcetypeassert // httptest supports flushing
		}
		_, _ = io.WriteString(w, big)
	}))
	defer srv.Close()
	s := newClient(t, safehttp.Options{MaxResponseBytes: 1024}, srv)
	s.net.name("example.com", "192.0.2.10")
	s.net.route("192.0.2.10", srv)
	// A declared length beyond the limit fails at once.
	resp, err := get(t, s.client, "https://example.com/declared")
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, safehttp.ErrResponseTooLarge) {
		t.Errorf("declared length: %v, want too large", err)
	}
	// Without a length the body ends with an error at the limit.
	resp, err = get(t, s.client, "https://example.com/chunked")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if !errors.Is(err, safehttp.ErrResponseTooLarge) || len(body) > 1024 {
		t.Errorf("chunked: %d bytes, %v; want at most 1024 and too large", len(body), err)
	}
}

func TestClient_TimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	s := newClient(t, safehttp.Options{Timeout: 100 * time.Millisecond}, srv)
	s.net.name("example.com", "192.0.2.10")
	s.net.route("192.0.2.10", srv)
	start := time.Now()
	resp, err := get(t, s.client, "https://example.com/slow")
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Errorf("slow target = %v after %v, want the deadline of the client", err, time.Since(start))
	}
}

// Webhook URLs carry secrets in path and query (Slack, Discord): errors name the host only.
func TestClient_ErrorsDoNotRevealTheURL(t *testing.T) {
	s := newClient(t, safehttp.Options{})
	s.net.name("hooks.example", "10.0.0.1")
	s.net.name("down.example", "192.0.2.77")
	for _, url := range []string{"https://hooks.example/services/T000/SECRET?token=abc", "https://down.example/services/T000/SECRET?token=abc"} {
		resp, err := get(t, s.client, url)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("GET %s succeeded", url)
		}
		if msg := err.Error(); strings.Contains(msg, "SECRET") || strings.Contains(msg, "token") || !strings.Contains(msg, ".example") {
			t.Errorf("error %q, want the host without path and query", msg)
		}
	}
}

// Proxy variables of the environment are ignored: a proxy would make the checks meaningless.
// (http.ProxyFromEnvironment reads them once per process, so the transport is checked directly.)
func TestClient_UsesNoProxy(t *testing.T) {
	if safehttp.HasProxy(safehttp.New(safehttp.Options{})) {
		t.Error("the transport has a proxy")
	}
}

func TestValidateURL(t *testing.T) {
	p := policy(t, "10.9.0.0/16")
	for _, good := range []string{"https://hooks.example/services/x", "http://ntfy.internal.example:8080/topic", "https://192.0.2.10/hook", "http://10.9.0.5/topic"} {
		if _, err := p.ValidateURL(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for bad, want := range map[string]error{
		"ftp://example.com/":                               safehttp.ErrInvalidURL,
		"file:///etc/passwd":                               safehttp.ErrInvalidURL,
		"https://user:pw@example.com/":                     safehttp.ErrInvalidURL,
		"https:///nohost":                                  safehttp.ErrInvalidURL,
		"https://example.com:99999/":                       safehttp.ErrInvalidURL,
		"//example.com/":                                   safehttp.ErrInvalidURL,
		"https://example.com/" + strings.Repeat("a", 4096): safehttp.ErrInvalidURL,
		"https://169.254.169.254/latest":                   safehttp.ErrBlocked,
		"https://[::ffff:10.0.0.1]/":                       safehttp.ErrBlocked,
	} {
		if _, err := p.ValidateURL(bad); !errors.Is(err, want) {
			t.Errorf("%.60s: %v, want %v", bad, err, want)
		}
	}
	// The parser's error would quote the URL with its secrets.
	if _, err := p.ValidateURL("https://hooks.example/services/%zzSECRET"); !errors.Is(err, safehttp.ErrInvalidURL) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("unparsable URL: %v, want invalid without the URL", err)
	}
	// Plain requests check the URL as well.
	s := newClient(t, safehttp.Options{})
	if resp, err := get(t, s.client, "https://user:pw@example.com/"); !errors.Is(err, safehttp.ErrInvalidURL) {
		if err == nil {
			resp.Body.Close()
		}
		t.Errorf("userinfo: %v", err)
	}
}

// The socket's own check (Dialer.Control) refuses blocked addresses, whatever dialed them.
func TestControl_ChecksTheSocketAddress(t *testing.T) {
	control := safehttp.Control(policy(t, "10.9.0.0/16"))
	for addr, blocked := range map[string]bool{"10.0.0.1:443": true, "[::1]:80": true, "192.0.2.10:443": false, "10.9.1.1:80": false, "bogus": true} {
		err := control("tcp", addr, nil)
		if blocked != errors.Is(err, safehttp.ErrBlocked) {
			t.Errorf("%s: %v, want blocked %v", addr, err, blocked)
		}
	}
}
