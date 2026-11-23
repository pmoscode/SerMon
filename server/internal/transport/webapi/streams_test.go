package webapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

var (
	streamOrg   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	streamGroup = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	streamNode  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	viewer      = authz.Principal{
		User: identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000ff")), Org: streamOrg,
		Bindings: []authz.Binding{{Group: streamGroup, Role: role.Viewer}},
	}
)

type oneNode struct{}

func (oneNode) List(context.Context, tenant.Scope) ([]fleet.Node, error) {
	return []fleet.Node{{ID: streamNode, Group: streamGroup, Hostname: "web-01"}}, nil
}

func (oneNode) HasGroup(context.Context, tenant.Scope, tenant.GroupID) (bool, error) {
	return true, nil
}

// fakeAuth stands in for the Authenticator: a fixed session and a changeable re-check result.
type fakeAuth struct {
	mu        sync.Mutex
	principal authz.Principal
	err       error
}

func (f *fakeAuth) set(p authz.Principal, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.principal, f.err = p, err
}

func (f *fakeAuth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }
func (f *fakeAuth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (f *fakeAuth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		id := conn.RequestHeader().Get("X-Session")
		ctx = context.WithValue(ctx, sessionKey{}, session.Session{ID: id, User: viewer.User, Org: viewer.Org, StepUpAt: time.Now()})
		ctx = context.WithValue(ctx, principalKey{}, viewer)
		ctx = context.WithValue(ctx, reauthKey{}, reauthenticator(func() (authz.Principal, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.principal, f.err
		}))
		return next(ctx, conn)
	}
}

func liveServer(t *testing.T, perSession int) (webv1connect.MetricsServiceClient, *fakeAuth) {
	t.Helper()
	store := memstore.New()
	store.AddNode(streamOrg, streamGroup, streamNode)
	ring := metrics.NewRing()
	svc := fleet.NewService(oneNode{}, metrics.NewReader(store, ring, time.Now), time.Now)
	handler := NewMetricsHandler(svc, nil, slog.New(slog.DiscardHandler))
	handler.streams = newStreamLimiter(streamLimits{Session: perSession, User: 10, Org: 10, Total: 10})
	handler.recheck = 0 // check on every message
	auth := &fakeAuth{principal: viewer}
	mux := http.NewServeMux()
	mux.Handle(webv1connect.NewMetricsServiceHandler(handler, connect.WithInterceptors(auth)))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return webv1connect.NewMetricsServiceClient(srv.Client(), srv.URL), auth
}

func watch(ctx context.Context, c webv1connect.MetricsServiceClient, sessionID string) (*connect.ServerStreamForClient[webv1.WatchMetricsResponse], error) {
	req := connect.NewRequest(&webv1.WatchMetricsRequest{Selector: &webv1.MetricSelector{
		Metric: "sermon_load_average", NodeIds: []string{streamNode.String()},
	}})
	req.Header().Set("X-Session", sessionID)
	return c.WatchMetrics(ctx, req)
}

// receive reads the first message; streams fail lazily, so errors show up here.
func receive(s *connect.ServerStreamForClient[webv1.WatchMetricsResponse]) error {
	if s.Receive() {
		return nil
	}
	if err := s.Err(); err != nil {
		return err
	}
	return io.EOF
}

func TestWatchMetrics_LimitsLiveStreamsPerSession(t *testing.T) {
	c, _ := liveServer(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var open []*connect.ServerStreamForClient[webv1.WatchMetricsResponse]
	for range 2 {
		s, err := watch(ctx, c, "session-a")
		if err != nil || receive(s) != nil {
			t.Fatalf("stream within the limit: %v", err)
		}
		open = append(open, s)
	}
	third, _ := watch(ctx, c, "session-a")
	if err := receive(third); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("third stream of a session: %v, want RESOURCE_EXHAUSTED", err)
	}
	other, _ := watch(ctx, c, "session-b")
	if err := receive(other); err != nil {
		t.Errorf("another session is not affected: %v", err)
	}

	_ = open[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		again, _ := watch(ctx, c, "session-a")
		err := receive(again)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a closed stream frees its slot: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWatchMetrics_EndsWhenTheSessionEnds(t *testing.T) {
	c, auth := liveServer(t, 4)
	// Without the re-check the stream would run for 15 minutes.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, err := watch(ctx, c, "session-a")
	if err != nil || receive(s) != nil {
		t.Fatalf("watch: %v", err)
	}

	auth.set(authz.Principal{}, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated))

	for s.Receive() {
	}
	if connect.CodeOf(s.Err()) != connect.CodeUnauthenticated {
		t.Errorf("stream after logout: %v, want UNAUTHENTICATED", s.Err())
	}
}

func TestWatchMetrics_EndsWhenTheRightsChange(t *testing.T) {
	c, auth := liveServer(t, 4)
	// Without the re-check the stream would run for 15 minutes.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, err := watch(ctx, c, "session-a")
	if err != nil || receive(s) != nil {
		t.Fatalf("watch: %v", err)
	}
	same := viewer
	same.Bindings = []authz.Binding{viewer.Bindings[0]}
	auth.set(same, nil)
	if !s.Receive() {
		t.Fatalf("unchanged rights must keep the stream: %v", s.Err())
	}

	fewer := viewer
	fewer.Bindings = nil
	auth.set(fewer, nil)

	for s.Receive() {
	}
	if err := s.Err(); err != nil {
		t.Errorf("stream after a rights change: %v, want a normal end", err)
	}
}

func TestSamePrincipal_IgnoresTheOrderOfBindings(t *testing.T) {
	other := tenant.GroupID(uuid.New())
	a := authz.Principal{User: viewer.User, Org: streamOrg, Bindings: []authz.Binding{{Group: streamGroup, Role: role.Viewer}, {Group: other, Role: role.Admin}}}
	b := authz.Principal{User: viewer.User, Org: streamOrg, Bindings: []authz.Binding{{Group: other, Role: role.Admin}, {Group: streamGroup, Role: role.Viewer}}}
	if !samePrincipal(a, b) {
		t.Error("same bindings in another order")
	}
	b.Bindings[0].Role = role.Viewer
	if samePrincipal(a, b) {
		t.Error("a changed role must count as a change")
	}
}

type memStore struct {
	mu sync.Mutex
	m  map[string]session.Session
}

func (s *memStore) Insert(_ context.Context, se session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[se.ID] = se
	return nil
}

func (s *memStore) Get(_ context.Context, id string) (session.Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.m[id]
	return se, ok, nil
}
func (s *memStore) Touch(context.Context, string, time.Time) error     { return nil }
func (s *memStore) SetStepUp(context.Context, string, time.Time) error { return nil }
func (s *memStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}
func (s *memStore) DeleteForUser(context.Context, identity.UserID) error { return nil }

type principals struct{ p authz.Principal }

func (f *principals) Load(context.Context, session.Session) (authz.Principal, error) { return f.p, nil }

type cookieConn struct {
	connect.StreamingHandlerConn
	header http.Header
}

func (c cookieConn) RequestHeader() http.Header { return c.header }

func TestAuthenticator_StreamsCanCheckTheirSessionAgain(t *testing.T) {
	sessions := session.NewManager(&memStore{m: map[string]session.Session{}}, time.Now)
	token, _, err := sessions.Create(t.Context(), viewer.User, viewer.Org)
	if err != nil {
		t.Fatal(err)
	}
	loader := &principals{p: viewer}
	a := NewAuthenticator(sessions, loader, slog.New(slog.DiscardHandler))
	var reauth reauthenticator
	handler := a.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		reauth, _ = ctx.Value(reauthKey{}).(reauthenticator)
		return nil
	})
	conn := cookieConn{header: http.Header{"Cookie": []string{SessionCookie + "=" + token}}}
	if err := handler(t.Context(), conn); err != nil || reauth == nil {
		t.Fatalf("handler: %v, re-check %v", err, reauth != nil)
	}

	loader.p.Bindings = nil
	if p, err := reauth(); err != nil || len(p.Bindings) != 0 {
		t.Errorf("re-check sees the current rights: %+v, %v", p, err)
	}
	if err := sessions.Revoke(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := reauth(); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("re-check after logout: %v, want UNAUTHENTICATED", err)
	}
}

// One user with many sessions, or one organization with many users, must not take all live
// streams of the instance.
func TestStreamLimiter_LimitsPerSessionUserOrganizationAndInstance(t *testing.T) {
	l := newStreamLimiter(streamLimits{Session: 2, User: 3, Org: 4, Total: 5})
	key := func(session, user, org string) streamKey { return streamKey{session: session, user: user, org: org} }

	for _, k := range []streamKey{key("s1", "u1", "o1"), key("s1", "u1", "o1"), key("s2", "u1", "o1")} {
		if !l.acquire(k) {
			t.Fatalf("%+v within the limits was refused", k)
		}
	}
	if l.acquire(key("s1", "u1", "o1")) {
		t.Error("a third stream of session s1 exceeds the session limit")
	}
	if l.acquire(key("s3", "u1", "o1")) {
		t.Error("a fourth stream of user u1 exceeds the user limit, also in a new session")
	}
	if !l.acquire(key("s4", "u2", "o1")) {
		t.Fatal("another user of o1 still gets a stream")
	}
	if l.acquire(key("s5", "u3", "o1")) {
		t.Error("a fifth stream of o1 exceeds the organization limit")
	}
	if !l.acquire(key("s6", "u4", "o2")) {
		t.Fatal("another organization still gets a stream")
	}
	if l.acquire(key("s7", "u5", "o3")) {
		t.Error("the sixth stream exceeds the instance limit")
	}

	l.release(key("s1", "u1", "o1"))
	if !l.acquire(key("s3", "u1", "o1")) {
		t.Error("released slots are free again")
	}
	if l.open() != 5 {
		t.Errorf("open = %d, want 5", l.open())
	}
}
