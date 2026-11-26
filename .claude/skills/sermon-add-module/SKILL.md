---
name: sermon-add-module
description: Add a built-in integration module to the SerMon agent (e.g. nginx, Apache, PostgreSQL, MySQL, Redis, Docker, TLS cert expiry, HTTP check, SMART, Prometheus scraper) with central configuration and default alerts. Use when asked to monitor a specific service/application.
---

# Add an integration module

Reference: KONZEPT.md §15, ADR 0039. Crate: `agent/crates/sermon-modules/src/<module>.rs` (or `<module>/`),
registered in `builtin()` (`src/lib.rs`), **disabled by default at runtime**. The runtime (`src/runtime.rs`) runs each
enabled module in its own task: time budget per run (≤ 30 s), ≤ 256 samples, panics and hangs stay with the module,
health `ok`/`error`/`timeout`/`hung`/`crashed`/`invalid_config`/`vetoed`. `src/simulate.rs` (feature `simulate`) is
the smallest complete example.

## Contract
```rust
pub trait Module: Send + Sync + 'static {
    fn id(&self) -> &'static str;                      // "nginx": metrics are sermon_nginx_…
    fn schema(&self) -> &'static str;                  // JSON schema, fixed subset (src/schema.rs) → UI form
    fn default_interval(&self) -> Duration;            // ≥ 10 s
    // config is already checked against the schema, defaults filled in; secrets in setup.secrets
    fn build(&self, config: &Config, setup: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError>;
    // a suggestion for the UI (every 10 min, nothing is activated); default None
    fn discover(&self, facts: &HostFacts) -> Option<Discovered>;
}
pub trait Instance: Send + 'static {
    fn timeout(&self) -> Duration;                     // budget of a run, default 5 s
    fn collect<'a>(&'a mut self, run: &'a Run) -> BoxFuture<'a, Result<Vec<Sample>, RunError>>;
}
```
- Everything reached goes through the veto (`setup.veto`): `resolve(host, port)` (connect to the returned addresses,
  not the name), `socket(path)`, `file(path)`. A refusal is `BuildError::Vetoed` / `RunError::Vetoed`.
- Blocking work only via `run.blocking(...)`, so the runtime sees a hung run.
- Error texts are plain, without credentials (they are redacted, but don't rely on it).

## Steps (TDD – `sermon-tdd`)
1. **Design metrics** with `sermon_<module>_<name>_<unit>` naming and bounded labels (see `sermon-add-collector`).
2. **Config schema:** endpoint (default localhost only), interval, credentials (marked secret → delivered encrypted, kept in memory only), TLS options. Validation tests first.
3. **Parser tests** with recorded real responses from supported versions in `agent/fixtures/modules/<module>/<version>/` (status pages, SQL result sets, Redis INFO, …).
4. **Collect:** strict timeout and response size limit; no shell-outs (exception: dedicated helper op, e.g. SMART → requires new fixed helper operation + ADR + security review). Errors are reported as module health, not panics; the runtime isolates each module.
5. **Least privilege guidance:** document the minimal permissions (e.g. PG role `pg_monitor`, MySQL `PROCESS, REPLICATION CLIENT`) in help text; warn loudly in UI when root-equivalent access is required (Docker socket).
6. **Discovery hint:** `discover()` with `Discovered::evidence(facts, &["nginx"], &["nginx.service"], &[80])` (a
   process or unit is required, ports add reasons), plus `suggested` (a JSON object ≤ 1 KiB for the form, e.g. the URL
   on `Listener::reach()`). Test with the recorded hosts in `agent/fixtures/discovery/` and fake `HostFacts`.
7. **Fuzz** every parser of foreign text in `agent/fuzz/fuzz_targets/module_parsers.rs` (first byte picks the parser)
   and seed it from the fixtures in `agent/fuzz/seed.sh`.
8. **Server:** the server knows a module from two files next to its code, copied by `just gen` (`gen-modules`) and
   checked by `just check-gen` – never edit the copies:
   - `schemas/<id>.json` → `server/internal/domain/module/schemas/`: the form, the server's check of a group's
     configuration (`module.Schema`, the same subset as `src/schema.rs`; both run `testdata/schema-vectors.json` –
     a new rule of the subset gets cases there) and the modules offered for a node (`Hello.modules`).
   - `metrics/<id>.json` → `server/internal/domain/metric/modules/`: the module's metrics, which the ingest drops
     otherwise: `{"metrics": [{"name": "sermon_<id>_…", "unit": "count", "aggregation": "last", "labels": ["…"]}]}`
     (units and aggregations of `metric.Unit`/`metric.Aggregation`, at most 8 labels, area `module`). In the module's
     tests call `crate::assert_in_catalog("<id>", &samples)`; `TestCatalog_ModuleMetrics` checks the file.
   - Help texts `docs/help/{de,en}/metrics/<metric>.md` for every metric (`TestCatalog_EveryMetricHasHelpTexts`) and
     `docs/help/{de,en}/modules/<module>.md`, labels of the form fields `module_<id>_<field>` (DE + EN), the module's
     tab in the UI shows its health and metrics.
9. **Default alerts** for the module via `sermon-add-alert-rule` (selector: nodes where module enabled).
10. **E2E:** add the service container to the E2E compose and a test that enables the module and sees metrics.

## Checklist
- [ ] Off by default; discovery only suggests
- [ ] Fixtures per supported version; timeouts + size limits
- [ ] Credentials secret-handled; minimal-privilege docs
- [ ] Catalog, UI, help DE + EN, default alerts, E2E
- [ ] `sermon-security-review` passed
