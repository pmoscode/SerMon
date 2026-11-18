---
name: sermon-security-review
description: SerMon-specific security checklist. Use before committing or opening a PR that touches authn/authz, tenant data, agent↔server protocol, enrollment/PKI, the root helper, log/process data, outgoing HTTP (notifiers, webhooks, modules), secrets, install/update scripts, or UI rendering of untrusted data. Also use when asked to "review security" of SerMon code.
---

# SerMon security review

Security is the top priority (KONZEPT.md §18). Walk through every section relevant to the diff (`git diff main...HEAD`). Report findings as: file:line – issue – concrete fix. Fix critical issues before continuing.

## 1. Identity & tenancy
- [ ] Server derives `org_id` / `node_id` **only from the mTLS client certificate** (agent API) or the session (web API) – never from payload fields.
- [ ] Every repository/query function takes a tenant scope (`tenant.Scope`) and filters by it; no "list all" without scope. PG RLS policy exists for new tables.
- [ ] Metric queries go only through the `MetricStore` port with a `tenant.Scope` (filters `series.org_id` + allowed `node_id`s); Loki queries set `X-Scope-OrgID`; user input is escaped/parameterized; no raw SQL/LogQL from clients.
- [ ] New RPC is added to the **role-matrix test** and **tenant-isolation test**. Default is deny.
- [ ] Cross-tenant access returns `NotFound` (don't leak existence).

## 2. Authentication & sessions
- [ ] Sensitive actions (updates, reboot, role changes, channel secrets, token creation) require **step-up auth**.
- [ ] Rate limiting on any new unauthenticated endpoint.
- [ ] Tokens (enrollment, recovery, API) are random ≥ 256 bit, stored as SHA-256 hash, compared in constant time, have TTL.

## 3. Agent & helper
- [ ] Agent still runs as `sermon` without capabilities; systemd hardening unchanged or tightened (`systemd-analyze security` < 2.0).
- [ ] Helper: only the fixed operation enum; no string → shell; arguments validated; `SO_PEERCRED` check intact; respects local veto (`allow_remote_updates`, `allow_remote_reboot`).
- [ ] No `std::process::Command` with user/server-controlled strings; no shell invocation (`sh -c`).
- [ ] Self-update/binary downloads verify signature against the **embedded release key**, not a server-provided key.
- [ ] New data sent from the agent passes **redaction** (cmdlines, log lines, command output).

## 4. Input handling & output encoding
- [ ] Inputs validated at the boundary (protovalidate rules / explicit checks), size-limited.
- [ ] Svelte: no `{@html}` with anything not 100 % static; log lines, hostnames, process names rendered as text; ANSI stripped.
- [ ] SQL only via sqlc / parameterized queries.
- [ ] Regexes from users use Rust `regex` (linear) or Go `regexp` (RE2) – never backtracking engines.
- [ ] YAML import validated against schema; dry-run before apply.

## 5. Outgoing requests (SSRF)
- [ ] Notifiers/modules/webhooks use the shared `safehttp` client: blocks private/loopback/link-local/metadata IPs unless allowlisted, resolves-and-pins IP, limited redirects, timeouts, response size limit.
- [ ] Webhook payloads signed (HMAC-SHA256 with timestamp).

## 6. Secrets
- [ ] Secrets stored AES-256-GCM encrypted (`secrets.Box`), never logged, never returned to the UI after saving.
- [ ] No secrets in error messages, panics, traces, test fixtures or commits (`gitleaks`).

## 7. Transport & PKI
- [ ] Agent channel TLS 1.3 only, client cert required; no `InsecureSkipVerify` / `danger_accept_invalid_certs` outside tests.
- [ ] Certificate lifetimes / denylist logic unchanged or tests updated.

## 8. Web hardening
- [ ] CSP not weakened (no `unsafe-inline`/`unsafe-eval`, no new external origins).
- [ ] State-changing requests go through Connect (non-simple content type) and the Origin check.
- [ ] Error responses are generic with a correlation ID.

## 9. Supply chain
- [ ] New dependency justified, maintained, license compatible with AGPL-3.0 (`cargo-deny`, `go-licenses`), pinned in lockfile.
- [ ] GitHub Actions pinned by commit SHA, minimal `permissions:`.
- [ ] Install scripts pass `shellcheck`, use `set -eu`, verify signatures, never `eval` downloaded content.

## 10. Privacy & audit
- [ ] New personal data (IPs, usernames, log content) has retention and role restrictions.
- [ ] Privileged actions write an audit log entry (who, what, when, result).

Then run: `just lint && just test && just security` (gosec, govulncheck, cargo-audit, cargo-deny, pnpm audit, gitleaks).
