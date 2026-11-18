// Package httpserver runs an HTTP server with protective timeouts and graceful shutdown.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Protective defaults against slow clients (Slowloris) and oversized headers.
// Streaming endpoints (live view, log tail) will extend deadlines per request via
// http.ResponseController instead of raising these global values.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10 // 64 KiB
)

// Server wraps http.Server with a context-driven lifecycle.
type Server struct {
	srv             *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
	onShutdown      func()
}

// New creates a Server. onShutdown is called once the stop signal arrives, before
// in-flight requests are drained (e.g. to fail readiness checks).
func New(handler http.Handler, logger *slog.Logger, shutdownTimeout time.Duration, onShutdown func()) *Server {
	return &Server{
		srv: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
		logger:          logger,
		shutdownTimeout: shutdownTimeout,
		onShutdown:      onShutdown,
	}
}

// HTTPServer exposes the underlying server for inspection in tests.
func (s *Server) HTTPServer() *http.Server {
	return s.srv
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down gracefully.
// It returns nil after a clean shutdown and an error if serving or draining fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server stopped unexpectedly: %w", err)
	case <-ctx.Done():
	}

	s.logger.InfoContext(ctx, "shutting down", "timeout", s.shutdownTimeout)
	s.onShutdown()

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		_ = s.srv.Close()
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}

	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server stopped unexpectedly: %w", err)
	}
	return nil
}
