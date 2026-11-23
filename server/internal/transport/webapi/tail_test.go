package webapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// emptyLogs is a log store without lines.
type emptyLogs struct{}

func (emptyLogs) Lines(context.Context, tenant.OrgID, logql.Query, time.Time, time.Time, int, bool) ([]logs.StoredLine, error) {
	return nil, nil
}

func (emptyLogs) Counts(context.Context, tenant.OrgID, logql.Query, time.Time, time.Time, time.Duration) ([]logs.CountSeries, error) {
	return nil, nil
}

// tailServer serves TailLogs and WatchMetrics with shared live stream limits; the tail checks the
// session in every round.
func tailServer(t *testing.T, perSession int) (webv1connect.LogServiceClient, webv1connect.MetricsServiceClient, *fakeAuth) {
	t.Helper()
	live := &LiveStreams{limiter: newStreamLimiter(streamLimits{Session: perSession, User: 10, Org: 10, Total: 10})}
	svc := logs.NewService(emptyLogs{}, oneNode{}, authz.NewPolicy(), logs.WithTailTiming(10*time.Millisecond, 0))
	store := memstore.New()
	store.AddNode(streamOrg, streamGroup, streamNode)
	metricsHandler := NewMetricsHandler(fleet.NewService(oneNode{}, metrics.NewReader(store, metrics.NewRing(), time.Now), time.Now), live, slog.New(slog.DiscardHandler))
	auth := &fakeAuth{principal: viewer}
	mux := http.NewServeMux()
	mux.Handle(webv1connect.NewLogServiceHandler(NewLogHandler(svc, live, slog.New(slog.DiscardHandler)), connect.WithInterceptors(auth)))
	mux.Handle(webv1connect.NewMetricsServiceHandler(metricsHandler, connect.WithInterceptors(auth)))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return webv1connect.NewLogServiceClient(srv.Client(), srv.URL), webv1connect.NewMetricsServiceClient(srv.Client(), srv.URL), auth
}

func tailLogs(ctx context.Context, c webv1connect.LogServiceClient, sessionID string) *connect.ServerStreamForClient[webv1.TailLogsResponse] {
	req := connect.NewRequest(&webv1.TailLogsRequest{NodeIds: []string{streamNode.String()}})
	req.Header().Set("X-Session", sessionID)
	s, _ := c.TailLogs(ctx, req)
	return s
}

func firstTail(s *connect.ServerStreamForClient[webv1.TailLogsResponse]) error {
	if s.Receive() {
		return nil
	}
	return s.Err()
}

// Tails and metric streams count against the same limits (T26).
func TestTailLogs_SharesTheLiveStreamLimits(t *testing.T) {
	logsClient, metricsClient, _ := tailServer(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watching, err := watch(ctx, metricsClient, "session-a")
	if err != nil || receive(watching) != nil {
		t.Fatalf("metric stream: %v", err)
	}
	if err := firstTail(tailLogs(ctx, logsClient, "session-a")); err != nil {
		t.Fatalf("tail within the limit: %v", err)
	}
	if err := firstTail(tailLogs(ctx, logsClient, "session-a")); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("third live stream of a session: %v, want RESOURCE_EXHAUSTED", err)
	}
	if err := firstTail(tailLogs(ctx, logsClient, "session-b")); err != nil {
		t.Errorf("another session is not affected: %v", err)
	}
}

func TestTailLogs_EndsWithTheSessionOrChangedRights(t *testing.T) {
	logsClient, _, auth := tailServer(t, 4)
	// Without the re-check the tail would run for 15 minutes.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s := tailLogs(ctx, logsClient, "session-a")
	if err := firstTail(s); err != nil {
		t.Fatal(err)
	}
	auth.set(authz.Principal{}, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated))
	for s.Receive() {
	}
	if connect.CodeOf(s.Err()) != connect.CodeUnauthenticated {
		t.Errorf("tail after logout: %v, want UNAUTHENTICATED", s.Err())
	}

	auth.set(viewer, nil)
	s = tailLogs(ctx, logsClient, "session-a")
	if err := firstTail(s); err != nil {
		t.Fatal(err)
	}
	fewer := viewer
	fewer.Bindings = nil
	auth.set(fewer, nil)
	for s.Receive() {
	}
	if err := s.Err(); err != nil || ctx.Err() != nil {
		t.Errorf("tail after a rights change: %v, want a normal end", err)
	}
}
