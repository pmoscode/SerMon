package agentapi_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
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

func (c *testCA) CertificateUsable(_ context.Context, serial string, _ tenant.OrgID, _ identity.NodeID) (bool, error) {
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
		Certificate: issued.DER, Serial: issued.Serial, Intermediate: issued.Intermediate, NotAfter: issued.NotAfter,
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
	s.sessions.RequestRotation(orgX, nodeX, func() { timedOut <- struct{}{}; s.sessions.Terminate(nodeX, rotation.ErrRotationTimeout) })
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

func (f fakeNodes) Connected(context.Context, tenant.OrgID, identity.NodeID, ingest.HostFacts, string, time.Time) (ingest.Connection, error) {
	return ingest.Connection{Group: f.group}, nil
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

// Revocation and expiry also end streams that are already open, not only new handshakes (the
// grace period of a rotation, an admin revoking a node, a certificate running out).
func TestSession_EndsWhenTheCertificateIsNoLongerUsable(t *testing.T) {
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithCertificateRecheck(50 * time.Millisecond)})
	cert, serial := s.ca.agentCert(t, orgX, nodeX)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := s.client(t, &cert, tls.VersionTLS13).Session(ctx)
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })
	time.Sleep(200 * time.Millisecond)
	if len(s.sessions.Active()) != 1 {
		t.Fatal("a usable certificate keeps its session")
	}

	s.ca.mu.Lock()
	s.ca.usable[serial] = false
	s.ca.mu.Unlock()

	_, err := receive(stream)
	for err == nil {
		_, err = receive(stream)
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("stream after revocation: %v, want PERMISSION_DENIED", err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 0 })
}

// After a normal rotation the session ends, so the agent reconnects with its new certificate at
// once; the reconnect is no second holder and locks nothing (M1-38).
func TestSession_EndsAfterARotationAndTheNewCertificateReconnects(t *testing.T) {
	s := startServer(t)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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
	newCert, _ := x509.ParseCertificate(issued.GetCertificate())
	s.ca.mu.Lock()
	s.ca.usable[hex.EncodeToString(newCert.SerialNumber.Bytes())] = true
	s.ca.mu.Unlock()
	if _, err := receive(stream); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("old session after the rotation: %v, want ABORTED (reconnect with the new certificate)", err)
	}

	fresh := tls.Certificate{Certificate: append([][]byte{issued.GetCertificate()}, issued.GetIntermediates()...), PrivateKey: key}
	next := s.client(t, &fresh, tls.VersionTLS13).Session(ctx)
	if err := next.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		a := s.sessions.Active()
		return len(a) == 1 && a[0].Serial == hex.EncodeToString(newCert.SerialNumber.Bytes())
	})
	if s.rotator.lockCount() != 0 {
		t.Error("the reconnect after a rotation must not lock the node")
	}
	_ = next.CloseRequest()
}

// recordingEvents is the event store of the session tests.
type recordingEvents struct {
	mu     sync.Mutex
	node   identity.NodeID
	events []secevent.Event
}

func (r *recordingEvents) Write(_ context.Context, _ tenant.OrgID, node identity.NodeID, batches []secevents.Batch) ([]secevents.Batch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.node = node
	for _, b := range batches {
		r.events = append(r.events, b.Events...)
	}
	return batches, nil
}

// Security events (M2-06/07): converted from the protocol, stored under the certificate's node,
// acknowledged after the write.
func TestSession_StoresSecurityEvents(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	config := ingest.DefaultConfig()
	config.FlushInterval = 20 * time.Millisecond
	service := ingest.NewService(store, metrics.NewRing(), fakeNodes{group: group}, config, time.Now, slog.New(slog.DiscardHandler))
	recorded := &recordingEvents{}
	service.SetEventWriter(recorded)
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithIngest(service)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Receive(); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	batch := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
		Seq: 3,
		Content: &agentv1.Batch_SecurityEvents{SecurityEvents: &agentv1.SecurityEventBatch{Events: []*agentv1.SecurityEvent{
			{OccurredAt: timestamppb.New(at), Event: &agentv1.SecurityEvent_SshLoginFailed{SshLoginFailed: &agentv1.SshLoginFailed{
				User: "root", Remote: &agentv1.SshRemote{Address: &agentv1.SshRemote_Ip{Ip: "::ffff:192.0.2.7"}, Port: 22},
				Method: agentv1.SshAuthMethod_SSH_AUTH_METHOD_KEYBOARD_INTERACTIVE, InvalidUser: true,
			}}},
			{OccurredAt: timestamppb.New(at), Event: &agentv1.SecurityEvent_SshLoginSucceeded{SshLoginSucceeded: &agentv1.SshLoginSucceeded{
				User: "alice", Remote: &agentv1.SshRemote{Address: &agentv1.SshRemote_Host{Host: "client.example.org"}},
				Method: agentv1.SshAuthMethod_SSH_AUTH_METHOD_PUBLICKEY, KeyType: "ED25519", KeyFingerprint: "SHA256:abc",
			}}},
			{OccurredAt: timestamppb.New(at), Event: &agentv1.SecurityEvent_SystemReboot{SystemReboot: &agentv1.SystemReboot{PreviousBootId: "cc8f", UptimeBefore: durationpb.New(time.Hour)}}},
			{OccurredAt: timestamppb.New(at), Event: &agentv1.SecurityEvent_AccountCreated{AccountCreated: &agentv1.AccountCreated{User: "bob", Uid: proto.Uint32(1001)}}},
			{OccurredAt: timestamppb.New(at), Event: &agentv1.SecurityEvent_OomKill{OomKill: &agentv1.OomKill{Pid: 42, Process: "python3"}}},
			// From a classic log file (ADR 0025).
			{OccurredAt: timestamppb.New(at), Unverified: true, Event: &agentv1.SecurityEvent_SudoNotAllowed{SudoNotAllowed: &agentv1.SudoNotAllowed{User: "carol", Target: "root", Tty: "pts/1"}}},
		}}},
	}}}
	if err := stream.Send(batch); err != nil {
		t.Fatal(err)
	}
	if res, err := stream.Receive(); err != nil || res.GetAck().GetSeq() != 3 {
		t.Fatalf("Receive() = %v, %v; want Ack 3", res, err)
	}
	_ = stream.CloseRequest()
	recorded.mu.Lock()
	defer recorded.mu.Unlock()
	if recorded.node != nodeX || len(recorded.events) != 6 {
		t.Fatalf("stored %d events for %v", len(recorded.events), recorded.node)
	}
	e := recorded.events
	if e[0].Kind != secevent.SSHLoginFailed || e[0].Remote.IP != netip.MustParseAddr("192.0.2.7") || e[0].Details.Method != "keyboard-interactive" ||
		!e[0].Details.InvalidUser || !e[0].OccurredAt.Equal(at) || e[0].Node != nodeX {
		t.Errorf("failed login = %+v", e[0])
	}
	if e[1].Remote.Host != "client.example.org" || e[1].Remote.Port != 0 || e[1].Details.KeyFingerprint != "SHA256:abc" {
		t.Errorf("login = %+v", e[1])
	}
	if e[2].Kind != secevent.SystemReboot || *e[2].Details.UptimeBefore != time.Hour || e[2].Details.PreviousBootID != "cc8f" {
		t.Errorf("reboot = %+v", e[2])
	}
	if *e[3].Details.UID != 1001 || e[4].Details.PID != 42 || e[4].Details.Process != "python3" || e[5].Details.Target != "root" || e[5].Details.TTY != "pts/1" {
		t.Errorf("details = %+v %+v %+v", e[3].Details, e[4].Details, e[5].Details)
	}
	if !e[5].Unverified || e[0].Unverified {
		t.Errorf("unverified = %v, %v; want only the event from the log file", e[0].Unverified, e[5].Unverified)
	}
}

// Log batches (M3-01) are stored from M3-07 on: until then the session acknowledges them, so an
// agent never resends them forever, and they touch neither metrics nor the ingest lag.
func TestSession_AcknowledgesLogBatchesWithoutStoringThemYet(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	config := ingest.DefaultConfig()
	config.FlushInterval = 20 * time.Millisecond
	service := ingest.NewService(store, metrics.NewRing(), fakeNodes{group: group}, config, time.Now, slog.New(slog.DiscardHandler))
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithIngest(service)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Receive(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(logs(journalEntry("upstream timed out"))); err != nil {
		t.Fatal(err)
	}
	if res, err := stream.Receive(); err != nil || res.GetAck().GetSeq() != 3 {
		t.Fatalf("Receive() = %v, %v; want Ack 3", res, err)
	}
	_ = stream.CloseRequest()
	if got := store.Aggregates(); len(got) != 0 {
		t.Errorf("stored %+v", got)
	}
	if lag := service.Stats().LagMillis.Load(); lag != 0 {
		t.Errorf("lag = %d ms after a log batch", lag)
	}
}

// recordingUpdates captures what the ingest writes as update reports.
type recordingUpdates struct {
	mu      sync.Mutex
	node    identity.NodeID
	reports []update.Report
}

func (r *recordingUpdates) Write(_ context.Context, _ tenant.OrgID, node identity.NodeID, report update.Report, _ time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.node = node
	r.reports = append(r.reports, report)
	return true, nil
}

func (r *recordingUpdates) Gauge(context.Context, tenant.OrgID, identity.NodeID) (update.Gauge, bool, error) {
	return update.Gauge{}, false, nil
}

// Update reports (M4-09): converted field by field, stored under the certificate's node.
func TestSession_StoresUpdateReports(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	config := ingest.DefaultConfig()
	config.FlushInterval = 20 * time.Millisecond
	service := ingest.NewService(store, metrics.NewRing(), fakeNodes{group: group}, config, time.Now, slog.New(slog.DiscardHandler))
	recorded := &recordingUpdates{}
	service.SetUpdateWriter(recorded)
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithIngest(service)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Receive(); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	upgraded := at.Add(-24 * time.Hour)
	batches := []*agentv1.Batch{
		{Seq: 4, Content: &agentv1.Batch_UpdateReport{UpdateReport: &agentv1.UpdateReport{
			CollectedAt: timestamppb.New(at), Availability: agentv1.UpdateAvailability_UPDATE_AVAILABILITY_LISTED,
			PackageManager: agentv1.PackageManager_PACKAGE_MANAGER_DNF, TotalCount: 3, SecurityCount: 1,
			Packages: []*agentv1.UpdatePackage{
				{Name: "vim-minimal", InstalledVersion: "9.1-1", CandidateVersion: "9.1-2", Source: "baseos"},
				{Name: "openssl", InstalledVersion: "3.2-1", CandidateVersion: "3.2-2", Source: "baseos", Security: true},
			},
			RebootRequired: true, RebootReason: "kernel", LastUpgradeAt: timestamppb.New(upgraded),
			Permissions: &agentv1.HelperPermissions{RemoteUpdates: true, SelfUpdate: true}, Refreshed: true,
			HelperVersion: "1.0.0-rc.1",
		}}},
		{Seq: 5, Content: &agentv1.Batch_UpdateReport{UpdateReport: &agentv1.UpdateReport{
			CollectedAt: timestamppb.New(at.Add(time.Second)), Availability: agentv1.UpdateAvailability_UPDATE_AVAILABILITY_NO_HELPER,
			Reason: "no helper",
		}}},
	}
	for _, b := range batches {
		if err := stream.Send(&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: b}}); err != nil {
			t.Fatal(err)
		}
		var acked uint64
		for acked < b.GetSeq() {
			res, err := stream.Receive()
			if err != nil || res.GetAck() == nil {
				t.Fatalf("Receive() = %v, %v; want an Ack", res, err)
			}
			acked = res.GetAck().GetSeq()
		}
	}
	_ = stream.CloseRequest()
	recorded.mu.Lock()
	defer recorded.mu.Unlock()
	if recorded.node != nodeX || len(recorded.reports) != 2 {
		t.Fatalf("stored %d reports for %v", len(recorded.reports), recorded.node)
	}
	r := recorded.reports[0]
	if !r.CollectedAt.Equal(at) || r.Availability != update.Listed || r.Manager != update.Dnf || r.Total != 3 || r.Security != 1 ||
		!r.RebootRequired || r.RebootReason != "kernel" || r.LastUpgradeAt == nil || !r.LastUpgradeAt.Equal(upgraded) || !r.Refreshed ||
		r.HelperVersion != "1.0.0-rc.1" {
		t.Errorf("report = %+v", r)
	}
	if r.Permissions == nil || *r.Permissions != (update.Permissions{RemoteUpdates: true, SelfUpdate: true}) {
		t.Errorf("permissions = %+v", r.Permissions)
	}
	want := update.Package{Name: "openssl", Installed: "3.2-1", Candidate: "3.2-2", Source: "baseos", Security: true}
	if len(r.Packages) != 2 || r.Packages[0] != want || r.Packages[1].Name != "vim-minimal" {
		t.Errorf("packages = %+v, want openssl first", r.Packages)
	}
	if n := recorded.reports[1]; n.Availability != update.NoHelper || n.Reason != "no helper" || n.Permissions != nil || n.LastUpgradeAt != nil || n.HelperVersion != "" {
		t.Errorf("report without helper = %+v", n)
	}
}

// M4-05: command results are part of the protocol before the server handles them (M4-11):
// acknowledged in order and dropped, without a metric batch's lag. Update reports without a
// writer likewise.
func TestSession_AcknowledgesUpdateReportsAndCommandResultsWithoutHandlingThemYet(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	config := ingest.DefaultConfig()
	config.FlushInterval = 20 * time.Millisecond
	service := ingest.NewService(store, metrics.NewRing(), fakeNodes{group: group}, config, time.Now, slog.New(slog.DiscardHandler))
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithIngest(service)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Receive(); err != nil {
		t.Fatal(err)
	}
	batches := []*agentv1.Batch{
		{Seq: 4, Content: &agentv1.Batch_UpdateReport{UpdateReport: &agentv1.UpdateReport{
			CollectedAt: timestamppb.Now(), Availability: agentv1.UpdateAvailability_UPDATE_AVAILABILITY_NO_HELPER,
		}}},
		{Seq: 5, Content: &agentv1.Batch_CommandResult{CommandResult: &agentv1.CommandResult{
			RunId: "0193f5a0-0000-7000-8000-000000000001", Phase: agentv1.CommandPhase_COMMAND_PHASE_STARTED,
		}}},
	}
	for _, b := range batches {
		if err := stream.Send(&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: b}}); err != nil {
			t.Fatal(err)
		}
	}
	var acked uint64
	for acked < 5 {
		res, err := stream.Receive()
		if err != nil || res.GetAck() == nil {
			t.Fatalf("Receive() = %v, %v; want an Ack", res, err)
		}
		acked = res.GetAck().GetSeq()
	}
	_ = stream.CloseRequest()
	if got := store.Aggregates(); len(got) != 0 {
		t.Errorf("stored %+v", got)
	}
	stats := service.Stats()
	if lag, rejected := stats.LagMillis.Load(), stats.Rejected.Load(); lag != 0 || rejected != 0 {
		t.Errorf("lag = %d ms, rejected %d after reports and results", lag, rejected)
	}
}

// recordingLogs captures what the ingest writes as logs.
type recordingLogs struct {
	mu      sync.Mutex
	node    identity.NodeID
	entries []ingest.LogEntry
}

func (r *recordingLogs) Write(_ context.Context, _ tenant.OrgID, node identity.NodeID, entries []ingest.LogEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.node = node
	r.entries = append(r.entries, entries...)
	return nil
}

// Log entries (M3-07): converted field by field, stored under the certificate's node.
func TestSession_StoresLogEntries(t *testing.T) {
	store := memstore.New()
	group := tenant.GroupID(uuid.New())
	store.AddNode(orgX, group, nodeX)
	config := ingest.DefaultConfig()
	config.FlushInterval = 20 * time.Millisecond
	service := ingest.NewService(store, metrics.NewRing(), fakeNodes{group: group}, config, time.Now, slog.New(slog.DiscardHandler))
	recorded := &recordingLogs{}
	service.SetLogWriter(recorded)
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithIngest(service)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(context.Background())
	if err := stream.Send(helloMsg()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Receive(); err != nil {
		t.Fatal(err)
	}
	journal := journalEntry("upstream timed out")
	at := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	journal.OccurredAt = timestamppb.New(at)
	file := &agentv1.LogEntry{
		OccurredAt: timestamppb.New(at), Source: agentv1.LogSource_LOG_SOURCE_FILE, File: "/var/log/nginx/error.log",
		Unit: "nginx", Message: "x", Truncated: true,
	}
	if err := stream.Send(logs(journal, file)); err != nil {
		t.Fatal(err)
	}
	if res, err := stream.Receive(); err != nil || res.GetAck().GetSeq() != 3 {
		t.Fatalf("Receive() = %v, %v; want Ack 3", res, err)
	}
	_ = stream.CloseRequest()
	recorded.mu.Lock()
	defer recorded.mu.Unlock()
	if recorded.node != nodeX || len(recorded.entries) != 2 {
		t.Fatalf("stored %d entries for %v", len(recorded.entries), recorded.node)
	}
	e := recorded.entries[0]
	if !e.At.Equal(at) || e.Source != ingest.LogSourceJournal || e.Unit != "nginx.service" || e.Identifier != "nginx" ||
		e.Severity != 4 || e.Message != "upstream timed out" || e.PID != 812 || e.Process != "nginx" || *e.UID != 33 ||
		e.BootID != "cc8f81a486f84386b00bc31030b4e1bb" || e.Transport != "stdout" || *e.Facility != 3 || e.Truncated {
		t.Errorf("journal entry = %+v", e)
	}
	f := recorded.entries[1]
	if f.Source != ingest.LogSourceFile || f.File != "/var/log/nginx/error.log" || !f.Truncated || f.UID != nil || f.Facility != nil {
		t.Errorf("file entry = %+v", f)
	}
}
