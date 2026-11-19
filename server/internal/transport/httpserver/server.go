// Package httpserver runs an HTTP server with protective timeouts and graceful shutdown.
package httpserver

import (
	"context"
	"crypto/tls"
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
	// HTTP/1.1 for browsers and HTTP/2 without TLS (h2c) for gRPC clients behind the
	// TLS-terminating reverse proxy (agents, ADR 0009). TLS itself ends at the proxy.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &Server{
		srv: &http.Server{
			Protocols:         protocols,
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

// Keepalive of long-lived streams (agent sessions): HTTP/2 pings detect dead connections.
const (
	streamPingInterval = 30 * time.Second
	streamPingTimeout  = 15 * time.Second
)

// NewStreaming creates a server for long-lived bidirectional streams (the mTLS agent API):
// no read/write deadlines (streams run for hours), HTTP/2 keepalive pings instead, and open
// streams are cancelled when the shutdown starts so that draining does not wait for them.
// With tlsConfig the server speaks TLS (HTTP/2 only); otherwise HTTP/1.1 and h2c (tests).
func NewStreaming(ctx context.Context, handler http.Handler, logger *slog.Logger, shutdownTimeout time.Duration, tlsConfig *tls.Config) *Server {
	// Streams outlive ctx's cancellation only until the shutdown cancels them.
	base, cancelStreams := context.WithCancel(context.WithoutCancel(ctx))
	protocols := new(http.Protocols)
	if tlsConfig != nil {
		protocols.SetHTTP2(true)
	} else {
		protocols.SetHTTP1(true)
		protocols.SetUnencryptedHTTP2(true)
	}
	srv := &http.Server{
		Handler:           handler,
		TLSConfig:         tlsConfig,
		Protocols:         protocols,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		HTTP2:             &http.HTTP2Config{SendPingTimeout: streamPingInterval, PingTimeout: streamPingTimeout},
		BaseContext:       func(net.Listener) context.Context { return base },
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(cancelStreams)
	return &Server{srv: srv, logger: logger, shutdownTimeout: shutdownTimeout, onShutdown: func() {}}
}

// HTTPServer exposes the underlying server for inspection in tests.
func (s *Server) HTTPServer() *http.Server {
	return s.srv
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down gracefully.
// It returns nil after a clean shutdown and an error if serving or draining fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		if s.srv.TLSConfig != nil {
			serveErr <- s.srv.ServeTLS(ln, "", "") // certificates come from TLSConfig
			return
		}
		serveErr <- s.srv.Serve(ln)
	}()

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
