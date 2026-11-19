// Command sermon-server runs the SerMon server: agent API, web API and embedded web UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
	"github.com/pmoscode/SerMon/server/internal/transport/health"
	"github.com/pmoscode/SerMon/server/internal/transport/httpserver"
	"github.com/pmoscode/SerMon/server/internal/transport/spa"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
	"github.com/pmoscode/SerMon/server/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = "usage: sermon-server [--version | init [--master-keys-file PATH]]"

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
	case len(args) > 0 && args[0] == "init":
		return runInit(ctx, args[1:], src, stdout, stderr)
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

// Login rate limit per client IP (brute-force protection, KONZEPT §7).
const (
	loginAttemptsPerWindow = 20
	loginWindow            = time.Minute
)

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	mux := http.NewServeMux()
	var checks []health.Check
	if cfg.DatabaseURL != "" {
		db, err := openDatabase(ctx, cfg, logger)
		if err != nil {
			return err
		}
		defer db.Close()
		checks = append(checks, health.Check{Name: "database", Fn: db.Ping})
		if _, err := agentca.Bootstrap(ctx, postgres.NewAgentCAStore(db), cfg.MasterKeys, time.Now); err != nil {
			return fmt.Errorf("agent CA: %w", err)
		}
		if err := mountAPI(ctx, mux, cfg, db, logger); err != nil {
			return err
		}
	} else {
		logger.WarnContext(ctx, "no database configured (SERMON_DATABASE_URL) – serving UI and health endpoints only")
	}
	healthHandler := health.New(logger, checks...)
	healthHandler.Register(mux)
	if err := mountUI(ctx, mux, logger); err != nil {
		return err
	}

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

func openDatabase(ctx context.Context, cfg config.Config, logger *slog.Logger) (*postgres.DB, error) {
	if cfg.DatabaseMigrateURL != "" {
		if err := postgres.Migrate(ctx, cfg.DatabaseMigrateURL); err != nil {
			return nil, fmt.Errorf("migrate database: %w", err)
		}
		logger.InfoContext(ctx, "database migrations applied")
	}
	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

// Setup attempts per client IP (the token has 256 bits; the limit keeps logs and DB quiet).
const (
	setupAttemptsPerWindow = 10
	setupWindow            = time.Minute
)

func mountAPI(ctx context.Context, mux *http.ServeMux, cfg config.Config, db *postgres.DB, logger *slog.Logger) error {
	if len(cfg.AllowedOrigins) == 0 {
		logger.WarnContext(ctx, "SERMON_ALLOWED_ORIGINS is empty – the web API rejects every state-changing request")
	}
	hasher, err := password.NewHasher(password.DefaultParams)
	if err != nil {
		return fmt.Errorf("password hasher: %w", err)
	}
	setupSvc := newSetupService(db, hasher)
	if err := announceSetup(ctx, setupSvc, logger); err != nil {
		return err
	}
	credentials := postgres.NewCredentialStore(db)
	sessions := session.NewManager(postgres.NewSessionStore(db), time.Now)
	login := auth.NewService(auth.Deps{
		Credentials: credentials,
		Sessions:    sessions,
		Hasher:      hasher,
		IPLimiter:   ratelimit.New(loginAttemptsPerWindow, loginWindow, time.Now),
		Now:         time.Now,
	})
	webapi.Mount(mux, webapi.Config{AllowedOrigins: cfg.AllowedOrigins, TrustedProxies: cfg.TrustedProxies}, webapi.Handlers{
		Auth:   webapi.NewAuthHandler(login, sessions, credentials, logger),
		System: webapi.NewSystemHandler(version),
		Access: webapi.NewAccessHandler(access.NewService(postgres.NewAccessStore(db), authz.NewPolicy(), time.Now, access.NewID),
			logger),
		Setup:         webapi.NewSetupHandler(setupSvc, ratelimit.New(setupAttemptsPerWindow, setupWindow, time.Now), logger),
		Authenticator: webapi.NewAuthenticator(sessions, access.NewLoader(postgres.NewAccessStore(db)), logger),
	})
	return nil
}

// mountUI serves the embedded web UI; without a UI build the server runs API-only.
func mountUI(ctx context.Context, mux *http.ServeMux, logger *slog.Logger) error {
	assets, err := web.Assets()
	if err != nil {
		return fmt.Errorf("load web UI: %w", err)
	}
	ui, err := spa.New(assets)
	if errors.Is(err, spa.ErrNoIndex) {
		logger.WarnContext(ctx, "web UI not built – serving API and health endpoints only")
		return nil
	}
	if err != nil {
		return fmt.Errorf("prepare web UI: %w", err)
	}
	registerUI(mux, ui)
	return nil
}

// registerUI mounts the UI on "/" without a method: "GET /" would conflict with the API's
// method-less prefix patterns. The UI handler itself only allows GET and HEAD.
func registerUI(mux *http.ServeMux, ui http.Handler) {
	mux.Handle("/", ui)
}

func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler = slog.NewJSONHandler(w, opts)
	if cfg.LogFormat == config.LogFormatText {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler).With("service", "sermon-server")
}
