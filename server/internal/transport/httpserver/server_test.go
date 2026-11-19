package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/transport/httpserver"
)

// testTimeout bounds every wait so a broken shutdown fails the test instead of hanging it.
const testTimeout = 5 * time.Second

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func get(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return http.DefaultClient.Do(req)
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestServe_ServesRequestsAndStopsGracefullyOnCancel(t *testing.T) {
	ln := listen(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })

	var shutdownCalled atomic.Bool
	srv := httpserver.New(mux, discardLogger(), time.Second, func() { shutdownCalled.Store(true) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	resp, err := get(t, "http://"+ln.Addr().String()+"/ping")
	if err != nil {
		t.Fatalf("GET /ping: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "pong" {
		t.Errorf("body = %q, want pong", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve() = %v, want nil after graceful shutdown", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Serve() did not return after cancel")
	}
	if !shutdownCalled.Load() {
		t.Error("onShutdown was not called")
	}
}

func TestServe_WaitsForInFlightRequests(t *testing.T) {
	ln := listen(t)
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	srv := httpserver.New(mux, discardLogger(), testTimeout, func() {})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	respCh := make(chan string, 1)
	go func() {
		resp, err := get(t, "http://"+ln.Addr().String()+"/slow")
		if err != nil {
			respCh <- "error: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		respCh <- string(b)
	}()

	<-entered
	cancel()
	close(release)

	select {
	case got := <-respCh:
		if got != "done" {
			t.Errorf("in-flight response = %q, want done", got)
		}
	case <-time.After(testTimeout):
		t.Fatal("in-flight request did not complete")
	}
	if err := <-done; err != nil {
		t.Errorf("Serve() = %v, want nil", err)
	}
}

func TestServe_ReturnsErrorWhenShutdownTimesOut(t *testing.T) {
	ln := listen(t)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stuck", func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	srv := httpserver.New(mux, discardLogger(), 50*time.Millisecond, func() {})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	go func() {
		if resp, err := get(t, "http://"+ln.Addr().String()+"/stuck"); err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-entered
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Serve() = nil, want error when shutdown times out")
		}
	case <-time.After(testTimeout):
		t.Fatal("Serve() did not return after shutdown timeout")
	}
}

func TestNew_SetsProtectiveTimeouts(t *testing.T) {
	srv := httpserver.New(http.NewServeMux(), discardLogger(), time.Second, func() {})
	hs := srv.HTTPServer()

	if hs.ReadHeaderTimeout <= 0 || hs.ReadTimeout <= 0 || hs.WriteTimeout <= 0 || hs.IdleTimeout <= 0 {
		t.Errorf("timeouts must be set: %+v", hs)
	}
	if hs.MaxHeaderBytes <= 0 || hs.MaxHeaderBytes > 1<<16 {
		t.Errorf("MaxHeaderBytes = %d, want 1..64KiB", hs.MaxHeaderBytes)
	}
}

// gRPC clients (agents, behind a TLS-terminating proxy) need HTTP/2 without TLS (ADR 0009).
func TestServer_AcceptsUnencryptedHTTP2(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var proto string
	srv := httpserver.New(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { proto = r.Proto }),
		slog.New(slog.DiscardHandler), time.Second, func() {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: protocols}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if proto != "HTTP/2.0" {
		t.Errorf("request protocol = %q, want HTTP/2.0 (h2c)", proto)
	}
}

// Long-lived agent streams must not block a graceful shutdown (KONZEPT §6, M1-10).
func TestStreamingServer_EndsOpenStreamsOnShutdown(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done() // a stream that only ends when the server says so
	})
	srv := httpserver.NewStreaming(context.Background(), handler, slog.New(slog.DiscardHandler), 5*time.Second, nil)
	if srv.HTTPServer().WriteTimeout != 0 || srv.HTTPServer().ReadTimeout != 0 || srv.HTTPServer().HTTP2 == nil ||
		srv.HTTPServer().HTTP2.SendPingTimeout != 15*time.Second { // KONZEPT §5
		t.Errorf("streaming server needs no read/write timeouts but HTTP/2 keepalive pings: %+v", srv.HTTPServer())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	go func() {
		if res, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}
	}()
	<-started
	begin := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve() = %v, want a clean shutdown", err)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Errorf("shutdown took %v; open streams must end right away", took)
	}
}
