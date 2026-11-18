---
name: sermon-add-collector
description: Add a new metric or metric collector to the SerMon Rust agent and wire it end-to-end (proto, server ingest, UI chart, help text, alert hooks). Use when asked to collect a new system metric (e.g. from /proc, /sys, statvfs) or extend an existing collector.
---

# Add a metric collector

Reference: KONZEPT.md §8 (naming, labels, cardinality), §19 (TDD).

## 1. Specify
- Metric names: `sermon_<area>_<name>_<unit>` following OpenTelemetry system conventions, Prometheus notation (`_bytes`, `_seconds`, `_ratio` for 0..1, `_per_second` for rates).
- Type: everything is sent as a **gauge**. Kernel counters are converted **in the agent** to per-second rates (`_per_second`) and ratios via deltas (handle resets/wrap) – see KONZEPT §8 / ADR 0001. Never send raw monotonic counters: TimescaleDB continuous aggregates can't compute rates (no window functions).
- Labels: only bounded ones (`cpu`, `device`, `interface`, `mountpoint`, `mode`). Never PIDs, usernames, paths from user data. Note expected series count per node; respect the per-node cardinality limit and default filters (`veth*`, `loop*`).
- Source file(s) and availability per distro / kernel version (graceful when missing → no metric, no error spam; log once at debug).

## 2. Agent (TDD – see `sermon-tdd`)
1. Capture real fixtures: copy the relevant `/proc`/`/sys` files from each target distro into `agent/fixtures/<distro>/...` (Debian, Ubuntu, Rocky, Alpine; plus arm64 if format differs).
2. Write parser tests first in `agent/crates/sermon-collectors/src/<area>/`:
   - pure `fn parse_<x>(input: &str) -> Result<Snapshot, ParseError>`
   - pure `fn compute(prev: &Snapshot, cur: &Snapshot, dt) -> Vec<Sample>` (handle counter resets/wrap)
3. Implement `Collector` trait; read via injected `ProcRoot`, never hard-coded `/proc`.
4. Add fuzz target in `agent/fuzz/fuzz_targets/parse_<x>.rs`.
5. Add a `criterion` bench if it runs every second; keep the whole agent within budget (< 1 % CPU, < 30 MB RSS).
6. Register in the collector registry; respect interval config.

## 3. Protocol / server
- Usually no proto change (generic `MetricBatch`). If new metadata is needed → use `sermon-proto-change`.
- Add the metric to the server's metric catalog (`server/internal/domain/metrics/catalog.go`): name, unit, type, labels, display name key, help key, default aggregation for charts on downsampled data (avg/max/last – the continuous aggregates `metrics_1m`/`metrics_1h` store all four of avg/min/max/last, the catalog decides which one the UI shows).
- No storage changes needed: new series are registered automatically in `series` (narrow schema, ADR 0001). Check the per-node cardinality budget instead.
- If it's an anomaly candidate, add it to the curated list in `server/internal/anomaly/candidates.go` with a test.

## 4. UI
- Add chart definition to the relevant detail page (`web/src/lib/charts/<area>.ts`) using shared uPlot wrapper and unit formatter.
- Help text: `docs/help/de/metrics/<metric>.md` **and** `docs/help/en/metrics/<metric>.md` (What is it? What's normal? What to do if high?). ⓘ tooltip pulls from there.
- i18n keys for display names in `web/messages/{de,en}.json`.

## 5. Alerts (optional)
If the metric warrants a default alert → use `sermon-add-alert-rule`.

## Checklist
- [ ] Fixtures for all distros, parser + compute tests, fuzz target
- [ ] Cardinality bounded and documented
- [ ] Catalog entry, aggregation, help texts DE+EN, chart
- [ ] `just lint && just test` green
