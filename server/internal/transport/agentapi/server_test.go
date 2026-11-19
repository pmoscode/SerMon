package agentapi_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
)

// testCA wraps a pki.Authority as agentapi.CA with a set of usable serials.
type testCA struct {
	*pki.Authority
	mu     sync.Mutex
	usable map[string]bool
}

func (c *testCA) IssueServerCertificate(hosts []string) (tls.Certificate, error) {
	return c.Authority.IssueServerCertificate(hosts, time.Now())
}

func (c *testCA) TrustAnchor() *x509.Certificate { return c.Root() }

func (c *testCA) CertificateUsable(_ context.Context, serial string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usable[serial], nil
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	a, err := pki.NewAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{Authority: a, usable: map[string]bool{}}
}

// agentCert issues a client certificate for org/node and marks it usable.
func (c *testCA) agentCert(t *testing.T, org tenant.OrgID, node identity.NodeID) (tls.Certificate, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	issued, err := c.SignAgentCSR(csr, org, node, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.usable[issued.Serial] = true
	c.mu.Unlock()
	return tls.Certificate{Certificate: [][]byte{issued.DER, c.Intermediate().Raw}, PrivateKey: key}, issued.Serial
}

type server struct {
	addr     string
	ca       *testCA
	sessions *agentapi.Sessions
}

func startServer(t *testing.T) server {
	t.Helper()
	ca := newTestCA(t)
	logger := slog.New(slog.DiscardHandler)
	sessions := agentapi.NewSessions()
	tlsConfig, err := agentapi.TLSConfig(ca, []string{"localhost"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentapi.Mount(mux, agentapi.NewSessionHandler(sessions, logger))
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return server{addr: ln.Addr().String(), ca: ca, sessions: sessions}
}

func (s server) client(t *testing.T, cert *tls.Certificate, tlsVersion uint16) agentv1connect.AgentServiceClient {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(s.ca.Root())
	cfg := &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tlsVersion, MaxVersion: tlsVersion}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}, Timeout: 10 * time.Second}
	return agentv1connect.NewAgentServiceClient(hc, "https://"+s.addr, connect.WithGRPC())
}

func helloMsg() *agentv1.SessionRequest { return hello(validHostFacts()) }

func heartbeatMsg() *agentv1.SessionRequest {
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{SentAt: timestamppb.Now()}}}
}

// runSession sends the messages, closes the request side and returns the stream's result.
func runSession(ctx context.Context, c agentv1connect.AgentServiceClient, msgs ...*agentv1.SessionRequest) error {
	stream := c.Session(ctx)
	for _, m := range msgs {
		if err := stream.Send(m); err != nil {
			break
		}
	}
	_ = stream.CloseRequest()
	for {
		if _, err := stream.Receive(); err != nil {
			_ = stream.CloseResponse()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

var (
	orgX  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	nodeX = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
)

func TestSession_AcceptsAValidAgentAndTakesTheIdentityFromTheCertificate(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	ctx := context.Background()

	stream := s.client(t, &cert, tls.VersionTLS13).Session(ctx)
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })
	active := s.sessions.Active()[0]
	if active.Org != orgX || active.Node != nodeX || active.AgentVersion != "0.0.0" {
		t.Errorf("active session = %+v", active)
	}
	if err := stream.Send(heartbeatMsg()); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseRequest()
	if _, err := stream.Receive(); !errors.Is(err, io.EOF) {
		t.Errorf("Receive() = %v, want EOF after the agent closed", err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 0 })
}

func TestTLS_RejectsAgentsWithoutAValidUsableCertificate(t *testing.T) {
	s := startServer(t)
	foreign := newTestCA(t)
	foreignCert, _ := foreign.agentCert(t, orgX, nodeX)
	revoked, serial := s.ca.agentCert(t, orgX, nodeX)
	s.ca.mu.Lock()
	s.ca.usable[serial] = false
	s.ca.mu.Unlock()
	valid, _ := s.ca.agentCert(t, orgX, nodeX)
	ctx := context.Background()

	cases := map[string]struct {
		cert    *tls.Certificate
		version uint16
	}{
		"no client certificate":     {nil, tls.VersionTLS13},
		"certificate of another CA": {&foreignCert, tls.VersionTLS13},
		"revoked certificate":       {&revoked, tls.VersionTLS13},
		"TLS 1.2":                   {&valid, tls.VersionTLS12},
	}
	for name, tc := range cases {
		err := runSession(ctx, s.client(t, tc.cert, tc.version), helloMsg())
		if err == nil {
			t.Errorf("%s: session accepted", name)
		}
	}
	if len(s.sessions.Active()) != 0 {
		t.Error("rejected agents must not get a session")
	}
}

func TestSession_RequiresHelloFirst(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	err := runSession(context.Background(), s.client(t, &cert, tls.VersionTLS13), heartbeatMsg())
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestSession_OnlyOneActiveSessionPerNode(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	second, _ := s.ca.agentCert(t, orgX, nodeX) // e.g. a stolen key used in parallel
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := s.client(t, &cert, tls.VersionTLS13).Session(ctx)
	if err := first.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })

	err := runSession(ctx, s.client(t, &second, tls.VersionTLS13), helloMsg())
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("second session: %v, want ALREADY_EXISTS", err)
	}
	if !strings.Contains(err.Error(), "active session") {
		t.Errorf("error message = %q", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A revoked certificate must not come back through TLS session resumption (gosec G123).
func TestTLS_RevocationAlsoAppliesToResumedSessions(t *testing.T) {
	s := startServer(t)
	cert, serial := s.ca.agentCert(t, orgX, nodeX)
	roots := x509.NewCertPool()
	roots.AddCert(s.ca.Root())
	cfg := &tls.Config{
		RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert},
		ClientSessionCache: tls.NewLRUClientSessionCache(4), NextProtos: []string{"h2"},
	}
	dial := func() (*tls.Conn, error) {
		conn, err := (&tls.Dialer{Config: cfg}).DialContext(context.Background(), "tcp", s.addr)
		if err != nil {
			return nil, err
		}
		tc, _ := conn.(*tls.Conn)
		// TLS 1.3 client certificates are checked after the handshake: a read surfaces the alert.
		_ = tc.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = tc.Read(make([]byte, 1))
		var netErr net.Error
		timedOut := errors.As(err, &netErr) && netErr.Timeout()
		if err != nil && !timedOut {
			_ = tc.Close()
			return nil, err
		}
		return tc, nil
	}
	first, err := dial()
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	_ = first.Close()

	s.ca.mu.Lock()
	s.ca.usable[serial] = false
	s.ca.mu.Unlock()
	second, err := dial()
	if err == nil {
		resumed := second.ConnectionState().DidResume
		_ = second.Close()
		t.Fatalf("revoked certificate accepted (resumed session: %v)", resumed)
	}
}
