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

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"

	"github.com/pmoscode/SerMon/server/internal/adapters/smtp"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/timescale"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/auth/twofactor"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/instanceaudit"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/platform/promtext"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
	"github.com/pmoscode/SerMon/server/internal/transport/download"
	"github.com/pmoscode/SerMon/server/internal/transport/health"
	"github.com/pmoscode/SerMon/server/internal/transport/httpserver"
	"github.com/pmoscode/SerMon/server/internal/transport/spa"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
	"github.com/pmoscode/SerMon/server/web"
)

var (
	// version is set at build time via -ldflags "-X main.version=...".
	version = "dev"
	// releaseKey is the minisign public key the installer checks agent binaries against; set at
	// build time (-X main.releaseKey=...), never at runtime (ADR 0012).
	releaseKey = ""
)

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
	if cfg.Mode == config.ModeDevelopment {
		logger.WarnContext(ctx, "development mode: SMTP optional (invitations prove no address), http:// origins and plain SMTP allowed – never use it in production")
	}
	mux := http.NewServeMux()
	var checks []health.Check
	var servers []func(context.Context) error
	registry := serverMetrics()
	if cfg.DatabaseURL != "" {
		db, err := openDatabase(ctx, cfg, logger)
		if err != nil {
			return err
		}
		defer db.Close()
		checks = append(checks, health.Check{Name: "database", Fn: db.Ping})
		ca, err := agentca.Bootstrap(ctx, postgres.NewAgentCAStore(db), cfg.MasterKeys, time.Now)
		if err != nil {
			return fmt.Errorf("agent CA: %w", err)
		}
		// Both listeners share the sessions: the web API reaches connected agents through them.
		sessions := agentapi.NewSessions(0)
		rotationStore := postgres.NewRotationStore(db)
		rotator := rotation.NewService(rotationStore, ca, time.Now, rotation.WithOnDemand(rotationStore, sessions, authz.NewPolicy()))
		// Live values (15 min, 1 s) and the stored 10 s windows (M1-22..M1-26).
		ring := metrics.NewRing()
		metricStore := timescale.New(db)
		reader := metrics.NewReader(metricStore, ring, time.Now)
		nodes := fleet.NewService(postgres.NewFleetStore(db), reader, time.Now)
		if err := mountAPI(ctx, mux, cfg, db, ca, rotator, nodes, registry, logger); err != nil {
			return err
		}
		servers = append(servers, refreshCA(ca, logger))
		ingestService := ingest.NewService(metricStore, ring, postgres.NewIngestNodeStore(db), ingest.DefaultConfig(), time.Now, logger)
		// Security events (M2-07): stored by the ingest, partitions kept by the maintenance.
		eventStore := postgres.NewSecurityEventStore(db)
		ingestService.SetEventWriter(eventStore)
		servers = append(servers, func(ctx context.Context) error {
			return secevents.Maintenance(ctx, eventStore, time.Now, 6*time.Hour, logger)
		})
		registerIngestMetrics(registry, ingestService.Stats(), sessions)
		servers = append(servers, pruneRing(ring))
		// Alert engine (M2-02): evaluates the rules of the organizations this instance leases – the
		// shipped defaults with each organization's overrides (M2-04).
		defaultRules, err := ruleset.Defaults()
		if err != nil {
			return fmt.Errorf("default alert rules: %w", err)
		}
		engine := alerting.NewEngine(alerting.Deps{
			Store: postgres.NewAlertStore(db), Rules: ruleset.NewSource(defaultRules, postgres.NewAlertRuleStore(db), logger),
			Nodes:   postgres.NewFleetStore(db),
			Metrics: reader, Now: time.Now, Logger: logger,
		})
		registerAlertMetrics(registry, engine.Stats())
		servers = append(servers, engine.Run)
		if cfg.AgentAddr != "" {
			agentServer, err := newAgentServer(ctx, cfg, sessions, rotator, ca, ingestService, logger)
			if err != nil {
				return err
			}
			servers = append(servers, agentServer)
		}
	} else {
		logger.WarnContext(ctx, "no database configured (SERMON_DATABASE_URL) – serving UI and health endpoints only")
	}
	healthHandler := health.New(logger, checks...)
	healthHandler.Register(mux)
	downloads, err := download.New(releaseKey, version, cfg.AgentDownloadsDir)
	if err != nil {
		return fmt.Errorf("agent downloads: %w", err)
	}
	if releaseKey == "" {
		logger.WarnContext(ctx, "built without release key – /install.sh refuses to install agents")
	}
	downloads.Register(mux)
	if err := mountUI(ctx, mux, logger); err != nil {
		return err
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	logger.InfoContext(ctx, "listening", "addr", ln.Addr().String(), "version", version)
	web := httpserver.New(webapi.StrictTransportSecurity(cfg.AllowedOrigins)(mux), logger, cfg.ShutdownTimeout, healthHandler.SetDraining)
	servers = append(servers, func(ctx context.Context) error { return web.Serve(ctx, ln) })
	if cfg.MetricsAddr != "" {
		metricsServer, err := newMetricsServer(ctx, cfg, registry, logger)
		if err != nil {
			return err
		}
		servers = append(servers, metricsServer)
	}
	return runAll(ctx, servers)
}

// pruneRing forgets ring series that stopped reporting (removed interfaces, offline nodes).
func pruneRing(ring *metrics.Ring) func(context.Context) error {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case now := <-ticker.C:
				ring.Prune(now)
			}
		}
	}
}

// newAgentServer prepares the mTLS agent API (M1-10) on its own listener.
func newAgentServer(ctx context.Context, cfg config.Config, sessions *agentapi.Sessions, rotator *rotation.Service,
	ca *agentca.Service, ingestService *ingest.Service, logger *slog.Logger,
) (func(context.Context) error, error) {
	tlsConfig, err := agentapi.TLSConfig(ca, cfg.AgentHostnames, logger) //nolint:contextcheck // TLS callbacks have no context
	if err != nil {
		return nil, fmt.Errorf("agent API TLS: %w", err)
	}
	mux := http.NewServeMux()
	agentapi.Mount(mux, agentapi.NewSessionHandler(sessions, rotator, ca, logger, agentapi.WithIngest(ingestService)))
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.AgentAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.AgentAddr, err)
	}
	logger.InfoContext(ctx, "agent API listening", "addr", ln.Addr().String(), "hosts", cfg.AgentHostnames)
	srv := httpserver.NewStreaming(ctx, mux, logger, cfg.ShutdownTimeout, tlsConfig)
	return func(ctx context.Context) error { return srv.Serve(ctx, ln) }, nil
}

// caRefreshInterval is how often every instance reloads the trusted agent CAs (a rollover by
// another instance becomes visible within this time).
const caRefreshInterval = 30 * time.Second

// refreshCA reloads the agent CAs periodically until ctx ends.
func refreshCA(ca *agentca.Service, logger *slog.Logger) func(context.Context) error {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(caRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := ca.Refresh(ctx); err != nil && ctx.Err() == nil {
					logger.ErrorContext(ctx, "reload agent CAs failed", "error", err)
				}
			}
		}
	}
}

// runAll runs the servers until ctx ends or one of them fails; then all of them stop.
func runAll(ctx context.Context, servers []func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(servers))
	for _, run := range servers {
		go func() {
			err := run(ctx)
			cancel() // one server stopping stops the others
			errs <- err
		}()
	}
	var first error
	for range servers {
		if err := <-errs; err != nil && first == nil {
			first = fmt.Errorf("serve: %w", err)
		}
	}
	return first
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

	// Invitation page requests per client IP (link tokens have 256 bits).
	invitationAttemptsPerWindow = 30
	invitationWindow            = time.Minute

	// Agent enrollments per client IP: mass rollouts often come from one NAT address; tokens
	// have 256 bits, so the limit is about load, not guessing.
	enrollmentsPerWindow = 120
	enrollmentWindow     = time.Minute
	// Wrong codes while setting up TOTP don't lock the account; this bounds guessing per user.
	totpSetupAttemptsPerWindow = 10
	totpSetupWindow            = time.Minute
)

func mountAPI(ctx context.Context, mux *http.ServeMux, cfg config.Config, db *postgres.DB, ca *agentca.Service,
	rotator *rotation.Service, nodes *fleet.Service, registry *promtext.Registry, logger *slog.Logger,
) error {
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
	policy := authz.NewPolicy()
	secondFactor := twofactor.NewService(postgres.NewTwoFactorStore(db), cfg.MasterKeys, policy, time.Now)
	login := auth.NewService(auth.Deps{
		Credentials:  credentials,
		Sessions:     sessions,
		Hasher:       hasher,
		IPLimiter:    ratelimit.New(loginAttemptsPerWindow, loginWindow, time.Now),
		Now:          time.Now,
		SecondFactor: secondFactor,
	})
	accessStore := postgres.NewAccessStore(db)
	principals := access.NewLoader(accessStore, secondFactor)
	metricsHandler := webapi.NewMetricsHandler(nodes, logger)
	registry.Gauge("sermon_server_live_streams", "Open live metric streams of the web UI.",
		func() float64 { return float64(metricsHandler.LiveStreams()) })
	var inviteOptions []invitation.Option
	var orgOptions []organization.Option
	if cfg.SMTP != nil {
		// Links go out by mail only: whoever accepts owns the mailbox (M1-39).
		mailer, base := newMailer(cfg.SMTP), cfg.AllowedOrigins[0]
		inviteOptions = append(inviteOptions, invitation.WithMailer(mailer, base))
		orgOptions = append(orgOptions, organization.WithMailer(mailer, base))
	} else {
		logger.WarnContext(ctx, "no SMTP configured: invitation links are shown in the UI and prove nothing (development only)")
	}
	invites := invitation.NewService(postgres.NewInvitationStore(db), credentials, policy, hasher, time.Now, access.NewID, inviteOptions...)
	webapi.Mount(mux, webapi.Config{AllowedOrigins: cfg.AllowedOrigins, TrustedProxies: cfg.TrustedProxies}, webapi.Handlers{
		Auth:   webapi.NewAuthHandler(login, sessions, credentials, secondFactor, logger).WithPrincipals(principals),
		System: webapi.NewSystemHandler(version),
		Access: webapi.NewAccessHandler(access.NewService(accessStore, policy, time.Now, access.NewID), invites, logger),
		Setup:  webapi.NewSetupHandler(setupSvc, ratelimit.New(setupAttemptsPerWindow, setupWindow, time.Now), logger),
		Invitation: webapi.NewInvitationHandler(invites, sessions,
			ratelimit.New(invitationAttemptsPerWindow, invitationWindow, time.Now), logger),
		Organization: webapi.NewOrganizationHandler(
			organization.NewService(postgres.NewOrganizationStore(db), policy, time.Now, access.NewID, orgOptions...), logger),
		Enrollment: webapi.NewEnrollmentTokenHandler(
			enrollment.NewService(postgres.NewEnrollmentTokenStore(db), policy, time.Now, access.NewID), logger),
		Node:    webapi.NewNodeHandler(rotator, nodes, logger),
		AgentCA: webapi.NewAgentCAHandler(ca, logger),
		InstanceAudit: webapi.NewInstanceAuditHandler(
			instanceaudit.NewService(postgres.NewInstanceAuditStore(db), policy), logger),
		Metrics: metricsHandler,
		Fleet:   webapi.NewFleetHandler(nodes, logger),
		SecurityEvents: webapi.NewSecurityEventHandler(
			secevents.NewService(postgres.NewSecurityEventStore(db), policy), logger),
		AgentEnrollment: webapi.NewAgentEnrollmentHandler(
			enrollment.NewEnroller(postgres.NewEnrollmentTokenStore(db), postgres.NewNodeRegistry(db), ca, time.Now, access.NewID),
			ratelimit.New(enrollmentsPerWindow, enrollmentWindow, time.Now), logger),
		TwoFactor: webapi.NewTwoFactorHandler(secondFactor, credentials,
			ratelimit.New(totpSetupAttemptsPerWindow, totpSetupWindow, time.Now), logger),
		Authenticator: webapi.NewAuthenticator(sessions, principals, logger),
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

// newMailer returns the SMTP mailer of the configuration.
func newMailer(c *config.SMTP) *smtp.Mailer {
	mode := map[config.SMTPTLS]smtp.TLSMode{config.SMTPStartTLS: smtp.StartTLS, config.SMTPImplicitTLS: smtp.ImplicitTLS, config.SMTPNone: smtp.NoTLS}[c.TLS]
	return smtp.New(smtp.Config{
		Host: c.Host, Port: c.Port, TLS: mode, From: c.From, FromName: c.FromName,
		Username: c.Username, Password: c.Password, RootCAs: c.RootCAs,
	})
}
