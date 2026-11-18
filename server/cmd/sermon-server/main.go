// Command sermon-server runs the SerMon server: agent API, web API and embedded web UI.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/transport/health"
	"github.com/pmoscode/SerMon/server/internal/transport/httpserver"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = "usage: sermon-server [--version]"

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], osSource(), os.Stdout, os.Stderr))
}

func osSource() config.Source {
	return config.Source{LookupEnv: os.LookupEnv, ReadFile: os.ReadFile}
}

// run is the testable entry point; it returns the process exit code.
func run(ctx context.Context, args []string, src config.Source, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "--version" || args[0] == "-V"):
		_, _ = fmt.Fprintf(stdout, "sermon-server %s\n", version)
		return exitOK
	case len(args) > 0:
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}

	cfg, err := config.Load(src)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
		return exitError
	}
	logger := newLogger(cfg, stdout)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, cfg, logger); err != nil {
		logger.ErrorContext(ctx, "server failed", "error", err)
		return exitError
	}
	logger.InfoContext(ctx, "stopped")
	return exitOK
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	healthHandler := health.New(logger)
	mux := http.NewServeMux()
	healthHandler.Register(mux)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	logger.InfoContext(ctx, "listening", "addr", ln.Addr().String(), "version", version)

	srv := httpserver.New(mux, logger, cfg.ShutdownTimeout, healthHandler.SetDraining)
	if err := srv.Serve(ctx, ln); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler = slog.NewJSONHandler(w, opts)
	if cfg.LogFormat == config.LogFormatText {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler).With("service", "sermon-server")
}
