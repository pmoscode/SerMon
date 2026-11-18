---
name: sermon-add-module
description: Add a built-in integration module to the SerMon agent (e.g. nginx, Apache, PostgreSQL, MySQL, Redis, Docker, TLS cert expiry, HTTP check, SMART, Prometheus scraper) with central configuration and default alerts. Use when asked to monitor a specific service/application.
---

# Add an integration module

Reference: KONZEPT.md §15. Crate: `agent/crates/sermon-modules/src/<module>/`, behind a Cargo feature (enabled in release builds), **disabled by default at runtime**.

## Contract
```rust
pub trait Module: Send + Sync {
    fn id(&self) -> &'static str;                      // "nginx"
    fn config_schema(&self) -> &'static str;           // JSON schema → UI form
    fn validate(&self, cfg: &serde_json::Value) -> Result<ModuleConfig, ConfigError>;
    fn discover(&self, host: &HostFacts) -> Option<DiscoveryHint>; // suggestion only, never auto-enable
    async fn collect(&self, ctx: &ModuleCtx) -> Result<ModuleOutput, ModuleError>;
}
```

## Steps (TDD – `sermon-tdd`)
1. **Design metrics** with `sermon_<module>_<name>_<unit>` naming and bounded labels (see `sermon-add-collector`).
2. **Config schema:** endpoint (default localhost only), interval, credentials (marked secret → delivered encrypted, kept in memory only), TLS options. Validation tests first.
3. **Parser tests** with recorded real responses from supported versions in `agent/fixtures/modules/<module>/<version>/` (status pages, SQL result sets, Redis INFO, …).
4. **Collect:** strict timeout and response size limit; no shell-outs (exception: dedicated helper op, e.g. SMART → requires new fixed helper operation + ADR + security review). Errors are reported as module health, not panics; the runtime isolates each module.
5. **Least privilege guidance:** document the minimal permissions (e.g. PG role `pg_monitor`, MySQL `PROCESS, REPLICATION CLIENT`) in help text; warn loudly in UI when root-equivalent access is required (Docker socket).
6. **Discovery hint:** detect via listening ports/process names/units → UI suggests enabling. Test with fake `HostFacts`.
7. **Server:** add metrics to the catalog, a module detail page/tab in the UI, help texts `docs/help/{de,en}/modules/<module>.md`.
8. **Default alerts** for the module via `sermon-add-alert-rule` (selector: nodes where module enabled).
9. **E2E:** add the service container to the E2E compose and a test that enables the module and sees metrics.

## Checklist
- [ ] Off by default; discovery only suggests
- [ ] Fixtures per supported version; timeouts + size limits
- [ ] Credentials secret-handled; minimal-privilege docs
- [ ] Catalog, UI, help DE + EN, default alerts, E2E
- [ ] `sermon-security-review` passed
