package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/updates"

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
		{"sermon_server_ingest_write_errors_total", "Failed writes to the metric, security event or update store.", stats.WriteErrors.Load},
		{"sermon_server_ingest_security_events_total", "Stored security events.", stats.Events.Load},
		{"sermon_server_ingest_dropped_security_events_total", "Security events dropped by the rate limits per node (all kinds).", stats.EventsDropped.Load},
		{"sermon_server_ingest_rejected_security_events_total", "Security events dropped for implausible times or invalid fields.", stats.EventsRejected.Load},
		{"sermon_server_ingest_duplicate_security_event_batches_total", "Security event batches stored before (resent after a reconnect).", stats.EventDuplicates.Load},
		{"sermon_server_ingest_log_entries_total", "Log entries stored in Loki.", stats.LogEntries.Load},
		{"sermon_server_ingest_uncounted_log_entries_total", "Log entries stored in Loki whose counts for the histogram were given up (V1-24).", stats.LogCountsDropped.Load},
		{"sermon_server_ingest_update_reports_total", "Stored update reports (M4-09).", stats.UpdateReports.Load},
		{"sermon_server_ingest_dropped_update_reports_total", "Update reports dropped by the limits per node and organization.", stats.UpdateReportsDropped.Load},
		{"sermon_server_ingest_rejected_update_reports_total", "Update reports dropped for implausible times, invalid fields or without a store.", stats.UpdateReportsRejected.Load},
		{"sermon_server_ingest_stale_update_reports_total", "Update reports not newer than the stored one (resent or out of order).", stats.UpdateReportsStale.Load},
	}
	for _, c := range counters {
		r.Counter(c.name, c.help, func() float64 { return float64(c.value()) })
	}
	// Per kind (M2-37): the noisy kinds are dropped under attack, the others should never be.
	r.CounterFamily("sermon_server_ingest_dropped_security_events_by_kind_total",
		"Security events dropped by the rate limits per node, per kind.", "kind", func() map[string]float64 {
			out := map[string]float64{}
			for k, n := range stats.DroppedEvents() {
				out[string(k)] = float64(n)
			}
			return out
		})
	// Log entries not stored, per reason (M3-07).
	r.CounterFamily("sermon_server_ingest_dropped_log_entries_total",
		"Log entries dropped: rate (limits per node and organization, or of the organization at Loki), age (time window), rejected (by Loki), unavailable (Loki down), no_store (no Loki configured).",
		"reason", func() map[string]float64 {
			return map[string]float64{
				"rate": float64(stats.LogsDroppedRate.Load()), "age": float64(stats.LogsDroppedAge.Load()),
				"rejected": float64(stats.LogsDroppedRejected.Load()), "unavailable": float64(stats.LogsDroppedUnavailable.Load()),
				"no_store": float64(stats.LogsDroppedNoStore.Load()),
			}
		})
	r.Gauge("sermon_server_ingest_lag_seconds", "Delay of the latest batch (server time minus collection time).",
		func() float64 { return float64(stats.LagMillis.Load()) / 1000 })
	r.Gauge("sermon_server_agent_sessions", "Connected agents.", func() float64 { return float64(len(sessions.Active())) })
}

// registerAlertMetrics adds the counters of the alert engine (M2-02).
func registerOrchestratorMetrics(r *promtext.Registry, stats *updates.OrchestratorStats) {
	for _, c := range []struct {
		name, help string
		value      func() uint64
	}{
		{"sermon_server_update_rounds_total", "Rounds of the update orchestrator.", stats.Rounds.Load},
		{"sermon_server_update_runs_started_total", "Update runs the orchestrator started.", stats.Started.Load},
		{"sermon_server_update_runs_failed_total", "Runs of rollouts that failed (update, reboot or health check).", stats.Failed.Load},
		{"sermon_server_update_rollouts_stopped_total", "Rollouts stopped at a failure.", stats.Stopped.Load},
		{"sermon_server_update_leases_lost_total", "Rounds dropped because another instance took the organization's lease meanwhile.", stats.LeasesLost.Load},
		{"sermon_server_update_errors_total", "Organizations whose round failed.", stats.Errors.Load},
	} {
		r.Counter(c.name, c.help, func() float64 { return float64(c.value()) })
	}
}

func registerAnomalyMetrics(r *promtext.Registry, stats *anomalies.Stats) {
	for _, c := range []struct {
		name, help string
		value      func() uint64
	}{
		{"sermon_server_anomaly_rounds_total", "Rounds of the anomaly detection.", stats.Rounds.Load},
		{"sermon_server_anomaly_scored_total", "Minutes of series the anomaly detection scored.", stats.Scored.Load},
		{"sermon_server_anomaly_notable_total", "Notable minutes the anomaly detection stored.", stats.Notable.Load},
		{"sermon_server_anomaly_series_skipped_total", "Candidate series left out over the limits per node, organization and instance.", stats.Skipped.Load},
		{"sermon_server_anomaly_baselines_total", "Hours of the week whose baselines were computed.", stats.Baselines.Load},
		{"sermon_server_anomaly_leases_lost_total", "Organizations another instance took over.", stats.LeasesLost.Load},
		{"sermon_server_anomaly_overruns_total", "Rounds that spent their time budget; the next one continues.", stats.Overruns.Load},
		{"sermon_server_anomaly_errors_total", "Organizations whose step failed.", stats.Errors.Load},
	} {
		r.Counter(c.name, c.help, func() float64 { return float64(c.value()) })
	}
}

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
		{"sermon_server_alert_org_timeouts_total", "Organizations whose evaluation ran out of its time budget.", stats.OrgTimeouts.Load},
		{"sermon_server_alert_leases_lost_total", "Rounds dropped because another instance took the organization's lease meanwhile.", stats.LeasesLost.Load},
	} {
		r.Counter(c.name, c.help, func() float64 { return float64(c.value()) })
	}
}

// registerNotifyMetrics adds the counters of the notification delivery (M2-10).
func registerNotifyMetrics(r *promtext.Registry, stats *notify.Stats) {
	for _, c := range []struct {
		name, help string
		value      func() uint64
	}{
		{"sermon_server_notify_sent_total", "Notifications delivered to a channel.", stats.Sent.Load},
		{"sermon_server_notify_failed_total", "Notifications that failed for good.", stats.Failed.Load},
		{"sermon_server_notify_retried_total", "Failed attempts that will be retried.", stats.Retried.Load},
		{"sermon_server_notify_blocked_total", "Attempts refused by the outbound policy (SSRF protection).", stats.Blocked.Load},
		{"sermon_server_notify_errors_total", "Delivery rounds that failed (database).", stats.Errors.Load},
		{"sermon_server_notify_claims_lost_total", "Attempts whose claim another attempt took over (outcome not stored).", stats.ClaimsLost.Load},
		{"sermon_server_notify_mails_deferred_total", "Mails held back by the mail limit of an organization.", stats.MailsDeferred.Load},
	} {
		r.Counter(c.name, c.help, func() float64 { return float64(c.value()) })
	}
}

// registerRoutingMetrics adds the counters of the alert routing (M2-14).
func registerRoutingMetrics(r *promtext.Registry, stats *routing.Stats) {
	r.Counter("sermon_server_routing_deliveries_total", "Notifications the routing queued for channels.",
		func() float64 { return float64(stats.Deliveries.Load()) })
	r.Counter("sermon_server_routing_errors_total", "Routing rounds that failed.",
		func() float64 { return float64(stats.Errors.Load()) })
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

// instanceHolder identifies this server instance for the leases of its workers.
func instanceHolder() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "orchestrator-" + hex.EncodeToString(raw)
}
