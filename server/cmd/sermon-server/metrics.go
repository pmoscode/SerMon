package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/platform/promtext"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
	"github.com/pmoscode/SerMon/server/internal/transport/httpserver"
)

// serverMetrics returns the registry of the server's own metrics with the runtime values (ADR 0011).
// Only instance values: no organization, group, node or host names.
func serverMetrics() *promtext.Registry {
	r := promtext.NewRegistry()
	r.Info("sermon_server_build_info", "Version of sermon-server.", map[string]string{"version": version, "go_version": runtime.Version()})
	r.Gauge("sermon_server_goroutines", "Running goroutines.", func() float64 { return float64(runtime.NumGoroutine()) })
	r.Gauge("sermon_server_heap_bytes", "Bytes of allocated heap objects.", func() float64 {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return float64(stats.HeapAlloc)
	})
	return r
}

// registerIngestMetrics adds the counters of the ingest (M1-24) and the connected agents.
func registerIngestMetrics(r *promtext.Registry, stats *ingest.Stats, sessions *agentapi.Sessions) {
	counters := []struct {
		name, help string
		value      func() uint64
	}{
		{"sermon_server_ingest_batches_total", "Accepted metric batches of agents.", stats.Batches.Load},
		{"sermon_server_ingest_samples_total", "Stored samples.", stats.Samples.Load},
		{"sermon_server_ingest_duplicate_batches_total", "Batches received again after a reconnect (acknowledged, not stored).", stats.Duplicates.Load},
		{"sermon_server_ingest_late_samples_total", "Samples dropped because their window was already written.", stats.Late.Load},
		{"sermon_server_ingest_rejected_samples_total", "Samples dropped for implausible times or the rate limit.", stats.Rejected.Load},
		{"sermon_server_ingest_unknown_samples_total", "Samples of metrics or labels missing from the catalog.", stats.Unknown.Load},
		{"sermon_server_ingest_dropped_series_total", "Samples dropped by the series limit per node.", stats.DroppedSeries.Load},
		{"sermon_server_ingest_write_errors_total", "Failed writes to the metric or security event store.", stats.WriteErrors.Load},
		{"sermon_server_ingest_security_events_total", "Stored security events.", stats.Events.Load},
		{"sermon_server_ingest_dropped_security_events_total", "Security events dropped by the rate limit per node.", stats.EventsDropped.Load},
		{"sermon_server_ingest_rejected_security_events_total", "Security events dropped for implausible times or invalid fields.", stats.EventsRejected.Load},
		{"sermon_server_ingest_duplicate_security_event_batches_total", "Security event batches stored before (resent after a reconnect).", stats.EventDuplicates.Load},
	}
	for _, c := range counters {
		r.Counter(c.name, c.help, func() float64 { return float64(c.value()) })
	}
	r.Gauge("sermon_server_ingest_lag_seconds", "Delay of the latest batch (server time minus collection time).",
		func() float64 { return float64(stats.LagMillis.Load()) / 1000 })
	r.Gauge("sermon_server_agent_sessions", "Connected agents.", func() float64 { return float64(len(sessions.Active())) })
}

// registerAlertMetrics adds the counters of the alert engine (M2-02).
func registerAlertMetrics(r *promtext.Registry, stats *alerting.Stats) {
	for _, c := range []struct {
		name, help string
		value      func() uint64
	}{
		{"sermon_server_alert_rounds_total", "Evaluation rounds of the alert engine.", stats.Rounds.Load},
		{"sermon_server_alert_evaluations_total", "Evaluated rules.", stats.Evaluations.Load},
		{"sermon_server_alert_transitions_total", "Stored state changes of alert instances.", stats.Transitions.Load},
		{"sermon_server_alert_rule_errors_total", "Rule evaluations that failed.", stats.RuleErrors.Load},
		{"sermon_server_alert_org_errors_total", "Organizations whose evaluation failed.", stats.OrgErrors.Load},
		{"sermon_server_alert_event_reads_truncated_total", "Event rule reads that hit the limit per round (a flood of events).", stats.EventReadsTruncated.Load},
	} {
		r.Counter(c.name, c.help, func() float64 { return float64(c.value()) })
	}
}

// newMetricsServer serves /metrics on its own listener without authentication (ADR 0011).
func newMetricsServer(ctx context.Context, cfg config.Config, registry *promtext.Registry, logger *slog.Logger) (func(context.Context) error, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.MetricsAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.MetricsAddr, err)
	}
	addr := ln.Addr().String()
	if host, _, err := net.SplitHostPort(addr); err != nil || !net.ParseIP(host).IsLoopback() {
		logger.WarnContext(ctx, "the metrics endpoint is reachable beyond this host and has no authentication", "addr", addr)
	}
	logger.InfoContext(ctx, "metrics listening", "addr", addr)
	mux := http.NewServeMux()
	mux.Handle("/metrics", registry.Handler())
	srv := httpserver.New(mux, logger, cfg.ShutdownTimeout, func() {})
	return func(ctx context.Context) error { return srv.Serve(ctx, ln) }, nil
}
