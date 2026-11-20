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
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
)

// testCA wraps pki.Authorities as agentapi.CA: the first one issues agent certificates,
// all of them are trusted (a rollover adds one); serials must be marked usable.
type testCA struct {
	*pki.Authority
	mu         sync.Mutex
	usable     map[string]bool
	extra      []*pki.Authority
	generation uint64
	now        func() time.Time
}

func (c *testCA) IssueServerCertificate(hosts []string) (tls.Certificate, error) {
	return c.Authority.IssueServerCertificate(hosts, c.clock())
}

func (c *testCA) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *testCA) TrustAnchors() []*x509.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	roots := []*x509.Certificate{c.Root()}
	for _, a := range c.extra {
		roots = append(roots, a.Root())
	}
	return roots
}

func (c *testCA) Intermediates() []*x509.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	inters := []*x509.Certificate{c.Intermediate()}
	for _, a := range c.extra {
		inters = append(inters, a.Intermediate())
	}
	return inters
}

func (c *testCA) Generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

// rollover adds a new trusted CA and returns it.
func (c *testCA) rollover(t *testing.T) *pki.Authority {
	t.Helper()
	a, err := pki.NewAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.extra = append(c.extra, a)
	c.generation++
	c.mu.Unlock()
	return a
}

// signWith issues an agent certificate with another authority and marks it usable.
func (c *testCA) signWith(t *testing.T, a *pki.Authority, org tenant.OrgID, node identity.NodeID) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	issued, err := a.SignAgentCSR(csr, org, node, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.usable[issued.Serial] = true
	c.mu.Unlock()
	return tls.Certificate{Certificate: [][]byte{issued.DER, a.Intermediate().Raw}, PrivateKey: key}
}

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

// fakeRotator records calls; rotations sign with the test CA.
type fakeRotator struct {
	ca        *testCA
	mu        sync.Mutex
	locked    []string
	rotated   []rotation.Identity
	immediate []bool
	rotateErr error
}

func (f *fakeRotator) SessionOpened(context.Context, rotation.Identity, string) (string, error) {
	return "", nil
}

func (f *fakeRotator) Rotate(_ context.Context, id rotation.Identity, csr []byte, _, _ string, immediate bool) (rotation.Rotated, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotated = append(f.rotated, id)
	f.immediate = append(f.immediate, immediate)
	if f.rotateErr != nil {
		return rotation.Rotated{}, f.rotateErr
	}
	issued, err := f.ca.SignAgentCSR(csr, id.Org, id.Node, time.Now())
	if err != nil {
		return rotation.Rotated{}, err
	}
	return rotation.Rotated{
		Certificate: issued.DER, Intermediate: f.ca.Intermediate().Raw, NotAfter: issued.NotAfter,
		PreviousValidUntil: time.Now().Add(rotation.Grace),
	}, nil
}

func (f *fakeRotator) Lock(_ context.Context, id rotation.Identity, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locked = append(f.locked, id.Node.String()+": "+reason)
	return nil
}

func (f *fakeRotator) lockCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.locked)
}

type server struct {
	addr     string
	ca       *testCA
	sessions *agentapi.Sessions
	rotator  *fakeRotator
}

func startServer(t *testing.T) server { return startServerWithTimeout(t, 0) }

func startServerWithTimeout(t *testing.T, rotationTimeout time.Duration) server {
	t.Helper()
	return startServerWith(t, newTestCA(t), rotationTimeout)
}

func startServerWith(t *testing.T, ca *testCA, rotationTimeout time.Duration, opts ...agentapi.TLSOption) server {
	t.Helper()
	return startServerFull(t, ca, rotationTimeout, opts, nil)
}

func startServerFull(t *testing.T, ca *testCA, rotationTimeout time.Duration, opts []agentapi.TLSOption, handlerOpts []agentapi.HandlerOption) server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	sessions := agentapi.NewSessions(rotationTimeout)
	rotator := &fakeRotator{ca: ca}
	tlsConfig, err := agentapi.TLSConfig(ca, []string{"localhost"}, logger, opts...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentapi.Mount(mux, agentapi.NewSessionHandler(sessions, rotator, ca, logger, handlerOpts...))
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return server{addr: ln.Addr().String(), ca: ca, sessions: sessions, rotator: rotator}
}

func (s server) client(t *testing.T, cert *tls.Certificate, tlsVersion uint16) agentv1connect.AgentServiceClient {
	t.Helper()
	roots := x509.NewCertPool()
	for _, r := range s.ca.TrustAnchors() {
		roots.AddCert(r)
	}
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
	if _, err := receive(stream); !errors.Is(err, io.EOF) {
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

func TestSession_ASecondHolderOfTheIdentityLocksItAndEndsBothSessions(t *testing.T) {
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
	if connect.CodeOf(err) != connect.CodeAlreadyExists || !strings.Contains(err.Error(), "locked") {
		t.Errorf("second session: %v, want ALREADY_EXISTS (locked)", err)
	}
	if s.rotator.lockCount() != 1 {
		t.Errorf("locks = %v, want one", s.rotator.locked)
	}
	if _, err := receive(first); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("first session: %v, want ABORTED", err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 0 })
}

func TestSession_TheSameAgentReconnectingReplacesItsOldSession(t *testing.T) {
	s := startServer(t)
	cert, serial := s.ca.agentCert(t, orgX, nodeX)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	old := s.client(t, &cert, tls.VersionTLS13).Session(ctx)
	if err := old.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })
	fresh := s.client(t, &cert, tls.VersionTLS13).Session(ctx)
	if err := fresh.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}

	if _, err := receive(old); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("old session: %v, want ABORTED (replaced)", err)
	}
	waitFor(t, func() bool { a := s.sessions.Active(); return len(a) == 1 && a[0].Serial == serial })
	if s.rotator.lockCount() != 0 {
		t.Error("a reconnect must not lock the node")
	}
}

func TestSession_RotationReturnsANewCertificateForTheSameIdentity(t *testing.T) {
	s := startServer(t)
	cert, serial := s.ca.agentCert(t, orgX, nodeX)
	ctx := context.Background()
	stream := s.client(t, &cert, tls.VersionTLS13).Session(ctx)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_RotateCertificate{
		RotateCertificate: &agentv1.RotateCertificate{Csr: csr},
	}}); err != nil {
		t.Fatal(err)
	}
	res, err := receive(stream)
	if err != nil {
		t.Fatal(err)
	}
	issued := res.GetCertificateIssued()
	newCert, err := x509.ParseCertificate(issued.GetCertificate())
	if err != nil {
		t.Fatal(err)
	}
	if org, node, err := pki.AgentIdentity(newCert); err != nil || org != orgX || node != nodeX {
		t.Errorf("new identity = %v/%v (%v)", org, node, err)
	}
	if len(issued.GetIntermediates()) != 1 || !issued.GetPreviousValidUntil().AsTime().After(time.Now()) {
		t.Errorf("response = %v", issued)
	}
	if got := s.rotator.rotated[0]; got.Serial != serial || got.Node != nodeX {
		t.Errorf("rotation identity = %+v, want the connection's certificate %s", got, serial)
	}
	_ = stream.CloseRequest()
}

func TestSession_RotationWithAReplacedCertificateEndsTheSession(t *testing.T) {
	s := startServer(t)
	s.rotator.rotateErr = rotation.ErrLocked
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)

	err := runSession(context.Background(), s.client(t, &cert, tls.VersionTLS13), helloMsg(),
		&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_RotateCertificate{RotateCertificate: &agentv1.RotateCertificate{Csr: csr}}})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("error = %v, want PERMISSION_DENIED", err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 0 })
}

// receive returns the next message after the trust bundle sent at session start.
func receive(stream *connect.BidiStreamForClient[agentv1.SessionRequest, agentv1.SessionResponse]) (*agentv1.SessionResponse, error) {
	for {
		res, err := stream.Receive()
		if err != nil || res.GetTrustBundle() == nil {
			return res, err
		}
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

func rotateMsg(t *testing.T) *agentv1.SessionRequest {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_RotateCertificate{RotateCertificate: &agentv1.RotateCertificate{Csr: csr}}}
}

func TestOnDemand_AgentRotatesImmediatelyAndTheSessionEnds(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	ctx := context.Background()
	stream := s.client(t, &cert, tls.VersionTLS13).Session(ctx)
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })

	timedOut := make(chan struct{}, 1)
	if !s.sessions.RequestRotation(orgX, nodeX, func() { timedOut <- struct{}{} }) {
		t.Fatal("the connected node must accept the request")
	}
	if s.sessions.RequestRotation(tenant.OrgID(uuid.New()), nodeX, func() {}) {
		t.Error("a request for another organization must not reach the node")
	}
	res, err := receive(stream)
	if err != nil || res.GetRotateNow() == nil {
		t.Fatalf("Receive() = %v, %v; want RotateNow", res, err)
	}
	if err := stream.Send(rotateMsg(t)); err != nil {
		t.Fatal(err)
	}
	if res, err := receive(stream); err != nil || res.GetCertificateIssued() == nil {
		t.Fatalf("Receive() = %v, %v; want CertificateIssued", res, err)
	}
	if _, err := receive(stream); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("after the on-demand rotation: %v, want ABORTED (reconnect)", err)
	}
	if len(s.rotator.immediate) != 1 || !s.rotator.immediate[0] {
		t.Errorf("immediate = %v, want the old certificate revoked at once", s.rotator.immediate)
	}
	select {
	case <-timedOut:
		t.Error("the timeout must not fire after a successful rotation")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestOnDemand_AgentThatDoesNotAnswerTimesOut(t *testing.T) {
	s := startServerWithTimeout(t, 100*time.Millisecond)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })

	timedOut := make(chan struct{}, 1)
	s.sessions.RequestRotation(orgX, nodeX, func() { timedOut <- struct{}{}; s.sessions.Terminate(nodeX) })
	select {
	case <-timedOut:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout did not fire")
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 0 })
	if s.sessions.RequestRotation(orgX, nodeX, func() {}) {
		t.Error("an offline node must report false")
	}
}

func TestSession_StartsWithTheTrustBundle(t *testing.T) {
	s := startServer(t)
	s.ca.rollover(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	res, err := stream.Receive()
	if err != nil || len(res.GetTrustBundle().GetRoots()) != 2 {
		t.Fatalf("first message = %v, %v; want the trust bundle with both roots", res, err)
	}
	_ = stream.CloseRequest()
}

func TestTLS_AcceptsAgentsOfEveryTrustedCA(t *testing.T) {
	s := startServer(t)
	newCA := s.ca.rollover(t)
	cert := s.ca.signWith(t, newCA, orgX, nodeX)
	if err := runSession(context.Background(), s.client(t, &cert, tls.VersionTLS13), helloMsg()); err != nil {
		t.Errorf("certificate of the new CA during the rollover: %v", err)
	}
}

func serverSerial(t *testing.T, s server, cert tls.Certificate) string {
	t.Helper()
	roots := x509.NewCertPool()
	for _, r := range s.ca.TrustAnchors() {
		roots.AddCert(r)
	}
	conn, err := (&tls.Dialer{Config: &tls.Config{
		RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, Time: s.ca.clock,
	}}).DialContext(context.Background(), "tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tc, _ := conn.(*tls.Conn)
	return tc.ConnectionState().PeerCertificates[0].SerialNumber.String()
}

func TestTLS_RenewsTheServerCertificateWithoutDroppingOpenSessions(t *testing.T) {
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	ca := newTestCA(t)
	ca.now = now
	s := startServerWith(t, ca, 0, agentapi.WithClock(now))
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	first := serverSerial(t, s, cert)

	open := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := open.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })

	mu.Lock()
	clock = clock.Add(pki.ServerCertificateLifetime * 3 / 4) // past two thirds
	mu.Unlock()
	if renewed := serverSerial(t, s, cert); renewed == first {
		t.Error("new handshakes must get a renewed server certificate")
	}
	if err := open.Send(heartbeatMsg()); err != nil {
		t.Errorf("the open session must keep running: %v", err)
	}
	if len(s.sessions.Active()) != 1 {
		t.Error("the open session was dropped")
	}
	_ = open.CloseRequest()
}

func TestTLS_NewServerCertificateWhenTheTrustedCAsChange(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	first := serverSerial(t, s, cert)
	if serverSerial(t, s, cert) != first {
		t.Fatal("without changes the server certificate is reused")
	}
	s.ca.rollover(t)
	if serverSerial(t, s, cert) == first {
		t.Error("a change of the trusted CAs must renew the server certificate")
	}
}

func TestSession_BatchesAreAcceptedButNotAcknowledgedWithoutIngest(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if res, err := stream.Receive(); err != nil || res.GetTrustBundle() == nil {
		t.Fatalf("first message = %v, %v", res, err)
	}
	for _, m := range []*agentv1.SessionRequest{batch(intSample("sermon_load_1m", nil)), heartbeatMsg()} {
		if err := stream.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	_ = stream.CloseRequest()
	// Without ingest nothing is persisted, so nothing may be acknowledged; the session stays healthy.
	res, err := stream.Receive()
	if !errors.Is(err, io.EOF) {
		t.Errorf("Receive() = %v, %v; want EOF without an Ack", res, err)
	}
}

// fakeNodes is the node directory of the ingest in these tests.
type fakeNodes struct{ group tenant.GroupID }

func (f fakeNodes) Connected(context.Context, tenant.OrgID, identity.NodeID, ingest.HostFacts, string, time.Time) (tenant.GroupID, error) {
	return f.group, nil
}

func (fakeNodes) Seen(context.Context, tenant.OrgID, identity.NodeID, time.Time) error { return nil }

func metricBatch(seq uint64, at time.Time, value int64) *agentv1.SessionRequest {
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
		Seq: seq,
		Content: &agentv1.Batch_Metrics{Metrics: &agentv1.MetricBatch{CollectedAt: timestamppb.New(at), Samples: []*agentv1.Sample{
			{Metric: "sermon_load_average", Labels: map[string]string{"period": "1m"}, Value: &agentv1.Sample_IntValue{IntValue: value}},
		}}},
	}}}
}

func TestSession_StoresBatchesUnderTheCertificateIdentityAndAcknowledgesThem(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	ring := metrics.NewRing()
	config := ingest.DefaultConfig()
	config.FlushInterval = 20 * time.Millisecond
	service := ingest.NewService(store, ring, fakeNodes{group: group}, config, time.Now, slog.New(slog.DiscardHandler))
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithIngest(service)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if res, err := stream.Receive(); err != nil || res.GetTrustBundle() == nil {
		t.Fatalf("first message = %v, %v", res, err)
	}

	// Two windows: the first one is complete once the second starts.
	start := time.Now().Add(-time.Minute).Truncate(10 * time.Second)
	for i := range 11 {
		if err := stream.Send(metricBatch(uint64(100+i), start.Add(time.Duration(i)*time.Second), int64(i))); err != nil {
			t.Fatal(err)
		}
	}
	res, err := stream.Receive()
	if err != nil || res.GetAck().GetSeq() != 109 {
		t.Fatalf("Receive() = %v, %v; want Ack 109 (last batch of the complete window)", res, err)
	}
	aggregates := store.Aggregates()
	if len(aggregates) != 1 || aggregates[0].Node != nodeX || aggregates[0].Avg != 4.5 {
		t.Errorf("stored = %+v", aggregates)
	}
	if seq, _ := store.PersistedSeq(context.Background(), orgX, nodeX); seq != 109 {
		t.Errorf("persisted seq = %d", seq)
	}
	scope, _ := tenant.ForAllGroups(orgX)
	if live := ring.Query(scope, metrics.Query{Metric: "sermon_load_average", Nodes: []identity.NodeID{nodeX}, From: start, To: start.Add(time.Minute), Step: time.Second}); len(live) != 1 || len(live[0].Points) != 11 {
		t.Errorf("ring = %+v", live)
	}

	// A resent batch is acknowledged at once and not stored again.
	if err := stream.Send(metricBatch(105, start.Add(5*time.Second), 5)); err != nil {
		t.Fatal(err)
	}
	if res, err := stream.Receive(); err != nil || res.GetAck().GetSeq() != 109 {
		t.Fatalf("duplicate: Receive() = %v, %v; want Ack 109", res, err)
	}
	_ = stream.CloseRequest()
	if got := len(store.Aggregates()); got != 1 {
		t.Errorf("aggregates after the duplicate = %d", got)
	}
}

func TestSession_AnInvalidBatchEndsTheSessionWithoutStoringAnything(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	service := ingest.NewService(store, metrics.NewRing(), fakeNodes{group: group}, ingest.DefaultConfig(), time.Now, slog.New(slog.DiscardHandler))
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithIngest(service)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	invalid := metricBatch(1, time.Now(), 1)
	invalid.GetBatch().GetMetrics().Samples[0].Metric = "evil'); DROP TABLE series; --"

	err := runSession(context.Background(), s.client(t, &cert, tls.VersionTLS13), helloMsg(), invalid)

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("session error = %v, want INVALID_ARGUMENT", err)
	}
	if len(store.Aggregates()) != 0 {
		t.Errorf("stored %+v", store.Aggregates())
	}
}

// A message above the limit ends the stream before it is decoded or validated (memory bound for
// agents, also with gzip).
func TestSession_LimitsTheMessageSize(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	facts := validHostFacts()
	facts.Hostname = strings.Repeat("a", agentapi.MaxMessageBytes+1)

	err := runSession(context.Background(), s.client(t, &cert, tls.VersionTLS13), hello(facts))

	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("hello with %d bytes = %v, want RESOURCE_EXHAUSTED", len(facts.GetHostname()), err)
	}
}
