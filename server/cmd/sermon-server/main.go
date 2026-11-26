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
	_ "time/tzdata" // time zones of maintenance windows without the host's zoneinfo (static binary)

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/update"

	"github.com/pmoscode/SerMon/server/internal/adapters/geoip"
	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	oidcadapter "github.com/pmoscode/SerMon/server/internal/adapters/oidc"
	"github.com/pmoscode/SerMon/server/internal/adapters/smtp"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/discord"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/email"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/gotify"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/mattermost"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/ntfy"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/pushover"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/slack"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/teams"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/telegram"
	"github.com/pmoscode/SerMon/server/internal/adapters/notify/webhook"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/timescale"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/auth/twofactor"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/instanceaudit"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/app/logsettings"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/modules"
	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/oidcmapping"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/app/orgretention"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/rules"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/app/selfmonitor"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/platform/promtext"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
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

const usage = "usage: sermon-server [--version | init [--master-keys-file PATH] | migrate]"

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
	case len(args) == 1 && args[0] == "migrate":
		return runMigrate(ctx, src, stdout, stderr)
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
	if len(cfg.OutboundAllowlist) > 0 {
		// Notification channels may reach these internal networks (safehttp, M2-09).
		logger.InfoContext(ctx, "outgoing requests may reach internal networks", "allowlist", fmt.Sprint(cfg.OutboundAllowlist))
	}
	mux := http.NewServeMux()
	var checks []health.Check
	var servers []func(context.Context) error
	registry := serverMetrics()
	// Logs (ADR 0031): the ingest writes them to Loki (M3-07).
	var logStore *loki.Client
	if cfg.LokiURL != nil {
		logStore = loki.New(cfg.LokiURL)
		checks = append(checks, health.Check{Name: "loki", Fn: logStore.Ready})
	} else {
		logger.WarnContext(ctx, "SERMON_LOKI_URL is not set – the server takes no logs from the agents")
	}
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
		// Notifications (M2-10): channels with sealed credentials, a delivery queue with retries;
		// every request to a channel goes through safehttp (M2-09).
		notifyDeps, err := newNotifyDeps(cfg, db, logger)
		if err != nil {
			return err
		}
		dispatcher := notify.NewDispatcher(notifyDeps)
		registerNotifyMetrics(registry, dispatcher.Stats())
		servers = append(servers, dispatcher.Run)
		// Routing (M2-14): notification policies decide which channels hear of an alert.
		routingStore := postgres.NewRoutingStore(db)
		// Silences and maintenance windows (M2-15) mute the routing for a while.
		policies := routing.NewService(routing.ServiceDeps{
			Store: routingStore, Windows: routingStore, Channels: postgres.NewNotificationStore(db), Groups: postgres.NewFleetStore(db),
			Policy: authz.NewPolicy(), Now: time.Now, NewID: access.NewID,
		})
		silences := routing.NewSilenceService(routing.SilenceDeps{
			Store: routingStore, Nodes: postgres.NewFleetStore(db), Policy: authz.NewPolicy(), Now: time.Now, NewID: access.NewID,
		})
		// The shipped default rules with each organization's overrides (M2-04).
		defaultRules, err := ruleset.Defaults()
		if err != nil {
			return fmt.Errorf("default alert rules: %w", err)
		}
		ruleSource := ruleset.NewSource(defaultRules, postgres.NewAlertRuleStore(db), logger)
		users := routing.UsersOf(postgres.NewAccessStore(db))
		router := routing.NewRouter(routing.RouterDeps{Store: routingStore, Rules: routing.RulesOf(ruleSource), Users: users, Logger: logger})
		alertStore := postgres.NewAlertStore(db)
		// The alert page (M2-16); acknowledgements go to the channels through the router.
		alertPage := alerts.NewService(alerts.Deps{
			Store: alertStore, Nodes: postgres.NewFleetStore(db), Rules: routing.RulesOf(ruleSource), Users: users, Mutes: routingStore,
			Sink: router, Policy: authz.NewPolicy(), Now: time.Now, Box: cfg.MasterKeys,
		})
		ruleEditor := rules.NewService(rules.Deps{
			Defaults: defaultRules, Store: postgres.NewAlertRuleStore(db), Policy: authz.NewPolicy(), Now: time.Now,
		})
		alertingAPI := alertingServices{policies: policies, silences: silences, alerts: alertPage, rules: ruleEditor}
		ingestService := ingest.NewService(metricStore, ring, postgres.NewIngestNodeStore(db), ingest.DefaultConfig(), time.Now, logger)
		// Self-monitoring (M2-24): ingest lag, dropped samples and database size, for superadmins only.
		if logStore != nil {
			ingestService.SetLogWriter(loki.NewWriter(logStore))
		}
		monitor := newSelfMonitor(cfg, db, ingestService, logger)
		servers = append(servers, func(ctx context.Context) error {
			monitor.Run(ctx, time.Minute)
			return nil
		})
		// Locking and deleting organizations (M2-33): ends the agent sessions here at once, mails
		// the admins, deletes after the grace.
		lifecycleOptions := []organization.LifecycleOption{organization.WithAgentSessions(sessions), organization.WithLogger(logger)}
		if cfg.SMTP != nil {
			lifecycleOptions = append(lifecycleOptions, organization.WithLifecycleMailer(newMailer(cfg.SMTP)))
		}
		// Purging an organization deletes its logs first (M3-10).
		if logStore != nil {
			lifecycleOptions = append(lifecycleOptions, organization.WithLogs(loki.NewEraser(logStore)))
		}
		lifecycle, err := organization.NewLifecycle(postgres.NewOrganizationLifecycleStore(db), authz.NewPolicy(), time.Now,
			cfg.OrgDeletionGrace, lifecycleOptions...)
		if err != nil {
			return fmt.Errorf("organization lifecycle: %w", err)
		}
		servers = append(servers, lifecycle.Run)
		// Deactivating and deleting nodes (M5-29, ADR 0041): ends the session here at once,
		// deletes after the grace, the logs first.
		nodeLifeOptions := []nodelife.Option{nodelife.WithLive(sessions), nodelife.WithLogger(logger)}
		if logStore != nil {
			nodeLifeOptions = append(nodeLifeOptions, nodelife.WithLogs(loki.NewEraser(logStore)))
		}
		nodeLife, err := nodelife.NewService(postgres.NewNodeLifecycleStore(db), authz.NewPolicy(), time.Now,
			cfg.NodeDeletionGrace, nodeLifeOptions...)
		if err != nil {
			return fmt.Errorf("node lifecycle: %w", err)
		}
		servers = append(servers, nodeLife.Run)
		// Retention per organization (M5-30): events within the hour, logs once a day in Loki.
		retentionOptions := []orgretention.Option{orgretention.WithLogger(logger)}
		if logStore != nil {
			retentionOptions = append(retentionOptions, orgretention.WithLogs(loki.NewEraser(logStore)))
		}
		orgRetention := orgretention.NewService(postgres.NewRetentionStore(db), authz.NewPolicy(), time.Now, retentionOptions...)
		servers = append(servers, orgRetention.Run)
		// Configuration of the agents (M3-03, ADR 0030): changes reach the connected sessions.
		agentConfigs := postgres.NewLogSettingsStore(db)
		logSettings := logsettings.NewService(agentConfigs, authz.NewPolicy(), sessions, time.Now)
		// The agent's modules (M5-06, ADR 0039): their schemas come from the agent's build.
		moduleCatalog, err := module.Builtin()
		if err != nil {
			return fmt.Errorf("module catalog: %w", err)
		}
		moduleStore := postgres.NewModuleStore(db)
		moduleSettings := modules.NewService(moduleStore, moduleCatalog, authz.NewPolicy(), sessions, time.Now)
		// The log explorer (M3-08); without Loki every query is UNAVAILABLE.
		var logReader logs.Store
		if logStore != nil {
			logReader = loki.NewReader(logStore)
		}
		logQueries := logs.NewService(logReader, postgres.NewFleetStore(db), authz.NewPolicy())
		// The live view of processes (M3-14): one agent stream per node shared by its viewers on
		// this instance; only the instance holding the agent's session reaches it (ADR 0015).
		liveHub := liveview.NewHub(sessions)
		liveViews := liveview.NewService(liveHub, postgres.NewFleetStore(db), agentConfigs, authz.NewPolicy())
		servers = append(servers, liveViews.Run)
		// Signed agent releases for the self-update (ADR 0036).
		releases, err := download.NewReleases(cfg.AgentDownloadsDir, releaseKey, version, logger)
		if err != nil {
			return fmt.Errorf("agent releases: %w", err)
		}
		servers = append(servers, releases.Run)
		observing := observingServices{logSettings: logSettings, modules: moduleSettings, logQueries: logQueries, liveViews: liveViews, releases: agentReleases{releases}}
		if err := mountAPI(ctx, mux, cfg, db, ca, rotator, nodes, notify.NewService(notifyDeps), alertingAPI, monitor, lifecycle, dataServices{nodes: nodeLife, retention: orgRetention}, observing, registry, logger); err != nil {
			return err
		}
		servers = append(servers, refreshCA(ca, logger))
		// Security events (M2-07): stored by the ingest, partitions kept by the maintenance.
		eventStore := postgres.NewSecurityEventStore(db)
		ingestService.SetEventWriter(eventStore)
		// Update reports (M4-09): the latest of each node with its packages.
		ingestService.SetUpdateWriter(postgres.NewUpdateStore(db, 0))
		// Health and discovery hints of the modules (M5-06).
		ingestService.SetModuleWriter(moduleStore, moduleCatalog)
		// Commands to the agents (M4-11): the instance with the node's session delivers them, a
		// NOTIFY of any instance wakes it, a poll catches what got lost.
		commandStore := postgres.NewCommandStore(db)
		ingestService.SetResultWriter(commandStore)
		servers = append(servers, func(ctx context.Context) error {
			db.Listen(ctx, postgres.CommandsChannel, 5*time.Second, func(payload string) {
				if id, err := uuid.Parse(payload); err == nil {
					sessions.CommandsWaiting(identity.NodeID(id))
				}
			}, func(err error) { logger.WarnContext(ctx, "listening for commands failed, retrying", "error", err) })
			return nil
		})
		servers = append(servers, func(ctx context.Context) error {
			return secevents.Maintenance(ctx, eventStore, time.Now, 6*time.Hour, logger)
		})
		registerIngestMetrics(registry, ingestService.Stats(), sessions)
		servers = append(servers, pruneRing(ring))
		// Alert engine (M2-02): evaluates the rules of the organizations this instance leases over
		// metrics and the stored security events (M2-08); the history is kept a year (M2-16).
		engine := alerting.NewEngine(alerting.Deps{
			Store: alertStore, Rules: ruleSource, Nodes: postgres.NewFleetStore(db),
			Metrics: reader, Events: eventStore, Sink: router, Now: time.Now, Logger: logger,
		})
		registerAlertMetrics(registry, engine.Stats())
		// Update orchestrator (M4-13): rolls the update policies out under a lease per organization.
		orchestratorConfig := updates.DefaultOrchestratorConfig(instanceHolder(), logger)
		orchestratorConfig.Releases = agentReleases{releases}
		orchestrator := updates.NewOrchestrator(postgres.NewRolloutStore(db), orchestratorConfig)
		registerOrchestratorMetrics(registry, orchestrator.Stats())
		servers = append(servers, orchestrator.Run)
		// Anomaly detection (M4-23, ADR 0037): every minute under a lease per organization.
		anomalyWorker := anomalies.NewWorker(postgres.NewAnomalyStore(db), metricStore, anomalies.DefaultConfig(instanceHolder(), logger))
		registerAnomalyMetrics(registry, anomalyWorker.Stats())
		servers = append(servers, anomalyWorker.Run)
		registerRoutingMetrics(registry, router.Stats())
		servers = append(servers, engine.Run, func(ctx context.Context) error {
			return alerting.Maintenance(ctx, alertStore, time.Now, 6*time.Hour, logger)
		})
		if cfg.AgentAddr != "" {
			agentServer, err := newAgentServer(ctx, cfg, sessions, rotator, ca, logger,
				agentapi.WithIngest(ingestService), agentapi.WithAgentConfig(agentConfigs), agentapi.WithLiveView(liveHub),
				agentapi.WithCommands(commandStore, agentapi.CommandPoll), agentapi.WithReleases(releases))
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
	repositories, err := download.NewRepository(cfg.RepoDir)
	if err != nil {
		return fmt.Errorf("package repositories: %w", err)
	}
	repositories.Register(mux)
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
	ca *agentca.Service, logger *slog.Logger, options ...agentapi.HandlerOption,
) (func(context.Context) error, error) {
	tlsConfig, err := agentapi.TLSConfig(ca, cfg.AgentHostnames, logger) //nolint:contextcheck // TLS callbacks have no context
	if err != nil {
		return nil, fmt.Errorf("agent API TLS: %w", err)
	}
	mux := http.NewServeMux()
	agentapi.Mount(mux, agentapi.NewSessionHandler(sessions, rotator, ca, logger, options...))
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

// observingServices are the use cases of logs and the live view behind the web API.
type observingServices struct {
	logSettings *logsettings.Service
	modules     *modules.Service
	logQueries  *logs.Service
	liveViews   *liveview.Service
	releases    updates.Releases
}

// agentReleases are the downloads' releases for the update use cases.
type agentReleases struct{ releases *download.Releases }

func (a agentReleases) Latest(channel update.Channel, arch string) (string, bool) {
	rel, ok := a.releases.Latest(channel.String(), arch)
	return rel.Version, ok
}

// alertingServices are the use cases of alerting behind the web API.
type alertingServices struct {
	policies *routing.Service
	silences *routing.SilenceService
	alerts   *alerts.Service
	rules    *rules.Service
}

// geoIPStale is the age of a country database the operator is reminded to update at.
const geoIPStale = 90 * 24 * time.Hour

// openGeoIP opens the operator's country database (M3-18); nothing is downloaded (D27).
func openGeoIP(ctx context.Context, cfg config.Config, logger *slog.Logger) (*geoip.DB, error) {
	db, err := geoip.Open(cfg.GeoIPDBFile, cfg.GeoIPAttribution)
	if err != nil {
		return nil, fmt.Errorf("SERMON_GEOIP_DB_FILE: %w", err)
	}
	built := db.BuildTime()
	logger.InfoContext(ctx, "countries of addresses from a local GeoIP database", "type", db.Type(), "built", built.Format(time.DateOnly))
	if time.Since(built) > geoIPStale {
		logger.WarnContext(ctx, "the GeoIP database is older than 90 days – countries may be wrong; replace the file (SERMON_GEOIP_DB_FILE) with a current one",
			"built", built.Format(time.DateOnly))
	}
	return db, nil
}

// dataServices delete the data of nodes and organizations (M5-29, M5-30).
type dataServices struct {
	nodes     *nodelife.Service
	retention *orgretention.Service
}

func mountAPI(ctx context.Context, mux *http.ServeMux, cfg config.Config, db *postgres.DB, ca *agentca.Service,
	rotator *rotation.Service, nodes *fleet.Service, notifications *notify.Service, alertingAPI alertingServices,
	health webapi.InstanceProblems, lifecycle *organization.Lifecycle, data dataServices, observing observingServices,
	registry *promtext.Registry, logger *slog.Logger,
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
	securityEvents := secevents.NewService(postgres.NewSecurityEventStore(db), policy)
	if cfg.GeoIPDBFile != "" {
		countries, err := openGeoIP(ctx, cfg, logger)
		if err != nil {
			return err
		}
		securityEvents.SetGeo(countries)
	}
	secondFactor := twofactor.NewService(postgres.NewTwoFactorStore(db), cfg.MasterKeys, policy, time.Now)
	login := auth.NewService(auth.Deps{
		Credentials:  credentials,
		Sessions:     sessions,
		Hasher:       hasher,
		IPLimiter:    ratelimit.New(loginAttemptsPerWindow, loginWindow, time.Now),
		Now:          time.Now,
		SecondFactor: secondFactor,
	})
	passkeys := newPasskeys(ctx, cfg, db, credentials, login, sessions, logger)
	secondFactor.WithPasskeys(passkeys)
	accessStore := postgres.NewAccessStore(db)
	principals := access.NewLoader(accessStore, secondFactor)
	live := webapi.NewLiveStreams()
	// The dashboards' reads of a fleet share half of the database pool (T106, M4-31).
	heavyReads := webapi.NewHeavyReads(db.PoolSize())
	metricsHandler := webapi.NewMetricsHandler(nodes, live, logger)
	registry.Gauge("sermon_server_live_streams", "Open live streams of the web UI (metrics, log tails and live views).",
		func() float64 { return float64(live.Open()) })
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
	authHandler := webapi.NewAuthHandler(login, sessions, credentials, secondFactor, logger).WithPrincipals(principals).
		WithInstanceHealth(accessStore, health).WithPasskeys(passkeys)
	mappings := oidcmapping.NewService(postgres.NewOIDCMappingStore(db), credentials, policy, time.Now, access.NewID, logger)
	if cfg.OIDC != nil {
		sso, err := newOIDC(ctx, cfg, db, sessions, credentials, secondFactor, mappings, logger)
		if err != nil {
			return err
		}
		authHandler.WithOIDC(sso, cfg.OIDC.Name)
	}
	webapi.Mount(mux, webapi.Config{AllowedOrigins: cfg.AllowedOrigins, TrustedProxies: cfg.TrustedProxies}, webapi.Handlers{
		Auth:   authHandler,
		System: webapi.NewSystemHandler(version),
		Access: webapi.NewAccessHandler(access.NewService(accessStore, policy, time.Now, access.NewID), invites, logger).
			WithRetention(data.retention),
		Setup: webapi.NewSetupHandler(setupSvc, ratelimit.New(setupAttemptsPerWindow, setupWindow, time.Now), logger),
		Invitation: webapi.NewInvitationHandler(invites, sessions,
			ratelimit.New(invitationAttemptsPerWindow, invitationWindow, time.Now), logger),
		Organization: webapi.NewOrganizationHandler(
			organization.NewService(postgres.NewOrganizationStore(db), policy, time.Now, access.NewID, orgOptions...), logger).
			WithLifecycle(lifecycle),
		Enrollment: webapi.NewEnrollmentTokenHandler(
			enrollment.NewService(postgres.NewEnrollmentTokenStore(db), policy, time.Now, access.NewID), logger),
		Node: webapi.NewNodeHandler(rotator, nodes, logger).WithTagger(fleet.NewTagger(postgres.NewFleetStore(db), policy, time.Now)).
			WithLifecycle(data.nodes),
		AgentCA: webapi.NewAgentCAHandler(ca, logger),
		InstanceAudit: webapi.NewInstanceAuditHandler(
			instanceaudit.NewService(postgres.NewInstanceAuditStore(db), policy), logger),
		Metrics:        metricsHandler,
		Fleet:          webapi.NewFleetHandler(nodes, logger),
		Capacity:       webapi.NewCapacityHandler(nodes, heavyReads, logger),
		Heatmap:        webapi.NewHeatmapHandler(nodes, heavyReads, logger),
		SecurityEvents: webapi.NewSecurityEventHandler(securityEvents, logger),
		Updates: webapi.NewUpdateHandler(updates.NewService(postgres.NewUpdateStore(db, 0), policy).
			WithRefresher(postgres.NewCommandStore(db), time.Now).
			WithManual(postgres.NewRolloutStore(db), postgres.NewUpdatePolicyStore(db), func() uuid.UUID { return uuid.Must(uuid.NewV7()) }).
			WithHistory(postgres.NewHistoryStore(db)).WithReleases(observing.releases), logger),
		UpdatePolicies: webapi.NewUpdatePolicyHandler(updates.NewPolicyService(postgres.NewUpdatePolicyStore(db), policy, time.Now), logger),
		Anomalies:      webapi.NewAnomalyHandler(anomalies.NewService(postgres.NewAnomalyStore(db), policy, time.Now), heavyReads, logger),
		AgentEnrollment: webapi.NewAgentEnrollmentHandler(
			enrollment.NewEnroller(postgres.NewEnrollmentTokenStore(db), postgres.NewNodeRegistry(db), ca, time.Now, access.NewID),
			ratelimit.New(enrollmentsPerWindow, enrollmentWindow, time.Now), logger),
		LogSettings:  webapi.NewLogSettingsHandler(observing.logSettings, logger),
		Modules:      webapi.NewModuleHandler(observing.modules, logger),
		Passkeys:     webapi.NewPasskeyHandler(passkeys, logger),
		OIDCMappings: webapi.NewOIDCMappingHandler(mappings, cfg.OIDC != nil, cfg.OIDC != nil && cfg.OIDC.JIT, logger),
		Logs:         webapi.NewLogHandler(observing.logQueries, live, logger),
		LiveView:     webapi.NewLiveViewHandler(observing.liveViews, live, logger),
		TwoFactor: webapi.NewTwoFactorHandler(secondFactor, credentials,
			ratelimit.New(totpSetupAttemptsPerWindow, totpSetupWindow, time.Now), logger),
		Notifications: webapi.NewNotificationChannelHandler(notifications, logger),
		Policies:      webapi.NewNotificationPolicyHandler(alertingAPI.policies, logger),
		Silences:      webapi.NewAlertSilenceHandler(alertingAPI.silences, logger),
		Windows:       webapi.NewMaintenanceWindowHandler(alertingAPI.policies, logger),
		Alerts:        webapi.NewAlertHandler(alertingAPI.alerts, logger),
		Rules:         webapi.NewAlertRuleHandler(alertingAPI.rules, logger),
		Dashboards:    webapi.NewDashboardHandler(dashboards.NewService(postgres.NewDashboardStore(db), policy, time.Now, access.NewID), logger),
		Authenticator: webapi.NewAuthenticator(sessions, principals, logger),
	})
	return nil
}

// Passkey ceremonies per client IP and window (start and finish, like logins).
const (
	passkeyAttemptsPerWindow = 30
	passkeyWindow            = time.Minute
)

// newPasskeys assembles the passkeys (M3-23, ADR 0034): the relying party is the host of the
// first allowed origin; without origins they are off.
func newPasskeys(ctx context.Context, cfg config.Config, db *postgres.DB, users passkey.Users, lockout passkey.Lockout,
	sessions *session.Manager, logger *slog.Logger,
) *passkey.Service {
	d := passkey.Deps{
		Store: postgres.NewPasskeyStore(db), Users: users, Lockout: lockout, Sessions: sessions,
		Limiter: ratelimit.New(passkeyAttemptsPerWindow, passkeyWindow, time.Now),
		// Answers count apart from starts (M3-29).
		FinishLimiter: ratelimit.New(passkeyAttemptsPerWindow, passkeyWindow, time.Now), Now: time.Now, Logger: logger,
	}
	// Passkeys never stop the server: without a usable origin they are off (a server reached by
	// its IP address once failed to start).
	rp, err := passkey.Config(cfg.AllowedOrigins)
	if err == nil {
		d.WebAuthn, err = webauthn.New(rp)
	}
	if err != nil {
		d.WebAuthn = nil
		logger.WarnContext(ctx, "passkeys are off: "+err.Error())
		return passkey.NewService(d)
	}
	logger.InfoContext(ctx, "passkeys", "rp_id", rp.RPID, "origins", fmt.Sprint(rp.RPOrigins))
	return passkey.NewService(d)
}

// oidcAttemptsPerWindow bounds the sign-ins at the provider per client IP (start and return).
const (
	oidcAttemptsPerWindow = 30
	oidcWindow            = time.Minute
)

// newOIDC assembles the sign-in at the OIDC provider (M3-20, ADR 0033). Discovery, keys and the
// token request go through the outbound policy: an internal provider needs the allowlist.
func newOIDC(ctx context.Context, cfg config.Config, db *postgres.DB, sessions *session.Manager, users oidc.Users,
	secondFactor oidc.SecondFactor, mappings oidc.Provisioner, logger *slog.Logger,
) (*oidc.Service, error) {
	outbound, err := safehttp.NewPolicy(cfg.OutboundAllowlist)
	if err != nil {
		return nil, fmt.Errorf("outbound policy: %w", err)
	}
	o := cfg.OIDC
	provider := oidcadapter.New(oidcadapter.Config{
		Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: o.RedirectURL, Scopes: o.Scopes,
	}, safehttp.New(safehttp.Options{Policy: outbound}).HTTPClient())
	if err := provider.Ready(ctx); err != nil {
		logger.WarnContext(ctx, "the OIDC provider can't be reached yet – sign-ins fail until it can", "issuer", o.Issuer, "error", err.Error())
	} else {
		logger.InfoContext(ctx, "sign-in over OIDC", "issuer", o.Issuer, "name", o.Name, "provisioning", o.JIT)
	}
	mfa := map[config.OIDCMFA]oidc.MFAMode{config.OIDCMFAByClaim: oidc.MFAByClaim, config.OIDCMFAAlways: oidc.MFAAlways, config.OIDCMFANever: oidc.MFANever}[o.MFA]
	store := postgres.NewOIDCStore(db)
	return oidc.NewService(oidc.Deps{
		Provider: provider, Flows: store, Identities: store, Users: users, Sessions: sessions, SecondFactor: secondFactor,
		Limiter: ratelimit.New(oidcAttemptsPerWindow, oidcWindow, time.Now),
		// Returns from the provider count apart from starts (M3-29).
		CompleteLimiter: ratelimit.New(oidcAttemptsPerWindow, oidcWindow, time.Now), MFA: mfa, Now: time.Now, Logger: logger,
		Provisioner: mappings, JIT: o.JIT,
	}), nil
}

// newNotifyDeps assembles the notifier framework. The HTTP channel types share one safehttp
// client of the outbound policy; e-mail exists only with SMTP. Deep links point to the first
// allowed origin of the UI.
func newNotifyDeps(cfg config.Config, db *postgres.DB, logger *slog.Logger) (notify.Deps, error) {
	outbound, err := safehttp.NewPolicy(cfg.OutboundAllowlist)
	if err != nil {
		return notify.Deps{}, fmt.Errorf("outbound policy: %w", err)
	}
	client := safehttp.New(safehttp.Options{Policy: outbound})
	notifiers := []notify.Notifier{
		webhook.New(client, time.Now), ntfy.New(client),
		slack.New(client), teams.New(client), discord.New(client), mattermost.New(client),
		telegram.New(client), gotify.New(client), pushover.New(client),
	}
	if cfg.SMTP != nil {
		notifiers = append(notifiers, email.New(newMailer(cfg.SMTP)))
	}
	registry, err := notify.NewRegistry(notifiers...)
	if err != nil {
		return notify.Deps{}, fmt.Errorf("notifiers: %w", err)
	}
	var base string
	if len(cfg.AllowedOrigins) > 0 {
		base = cfg.AllowedOrigins[0]
	}
	return notify.Deps{
		Store: postgres.NewNotificationStore(db), Registry: registry, Box: cfg.MasterKeys, Policy: authz.NewPolicy(),
		Outbound: outbound, BaseURL: base, Now: time.Now, NewID: access.NewID, Logger: logger,
	}, nil
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

// selfSources are the measurements of the self-monitoring (M2-24).
type selfSources struct {
	stats *ingest.Stats
	logs  *ingest.Service
	store *postgres.SelfMonitorStore
}

func (s selfSources) IngestLag() time.Duration {
	return time.Duration(s.stats.LagMillis.Load()) * time.Millisecond
}

// DroppedSamples sums what the ingest dropped: implausible times and the rate limit, windows
// written already, unknown metrics and the series limit.
func (s selfSources) DroppedSamples() uint64 {
	return s.stats.Rejected.Load() + s.stats.Late.Load() + s.stats.Unknown.Load() + s.stats.DroppedSeries.Load()
}

// DroppedRareEvents counts the rare security events the ingest dropped (M2-37).
func (s selfSources) DroppedRareEvents() uint64 { return s.stats.DroppedRareEvents() }

// LogsUnavailable reports whether the ingest counts Loki as down (M3-07).
func (s selfSources) LogsUnavailable() bool { return s.logs.LogsUnavailable() }

func (s selfSources) DatabaseSize(ctx context.Context) (int64, error) {
	size, err := s.store.DatabaseSize(ctx)
	if err != nil {
		return 0, fmt.Errorf("self-monitoring: %w", err)
	}
	return size, nil
}

// newSelfMonitor returns the self-monitoring; superadmins get mail only with SMTP.
func newSelfMonitor(cfg config.Config, db *postgres.DB, ingestService *ingest.Service, logger *slog.Logger) *selfmonitor.Monitor {
	store := postgres.NewSelfMonitorStore(db)
	deps := selfmonitor.Deps{
		Sources: selfSources{stats: ingestService.Stats(), logs: ingestService, store: store}, DatabaseLimit: cfg.DatabaseSizeLimit, Now: time.Now, Logger: logger,
	}
	if cfg.SMTP != nil {
		deps.Admins, deps.Mailer = store, newMailer(cfg.SMTP)
	}
	return selfmonitor.New(deps)
}
