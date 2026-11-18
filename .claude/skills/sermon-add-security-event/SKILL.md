---
name: sermon-add-security-event
description: Add or extend a security event parser in the SerMon agent that turns journald/auth.log lines into structured events (ssh logins, sudo, account changes, OOM, reboot). Use when asked to detect a new security-relevant log pattern or to fix parsing for a distro/OpenSSH version.
---

# Add a security event parser

Reference: KONZEPT.md §11. Crate: `agent/crates/sermon-security/`.

## 1. Collect real samples first
- Gather **real** log lines for every target distro and relevant software versions (e.g. OpenSSH < 9.8 logs as `sshd`, ≥ 9.8 as `sshd-session`), both journald JSON and classic `/var/log/auth.log` / `/var/log/secure` formats.
- Include IPv4, IPv6, `invalid user`, usernames with spaces/unicode, unusual ports, PAM variants.
- Store them in `agent/fixtures/logs/<distro>/<source>.log` and journald JSON in `…/<source>.jsonl`. **Anonymize** real IPs/usernames (use 192.0.2.0/24, 2001:db8::/32, `alice`, `bob`).

## 2. Define the event
- Event type `<domain>.<subject>.<result>` (e.g. `ssh.login.failed`). Add to the proto enum/schema only via `sermon-proto-change` if new.
- Fields: always `ts` (from log, not receive time), plus typed fields (`user: String`, `ip: IpAddr`, `port: u16`, `method: AuthMethod`). Never pass the raw line as an event field (logs are shipped separately and redacted).

## 3. TDD the parser (see `sermon-tdd`)
- Pure function: `fn parse(entry: &LogEntry) -> Option<SecurityEvent>`; match on `SYSLOG_IDENTIFIER`/`_COMM` first, then message.
- Use `regex` crate (linear time) with anchored patterns compiled once (`LazyLock`). Prefer explicit patterns per message type over one giant regex.
- Table-driven tests over all fixture lines: expected event or `None`. Lines that must **not** match are just as important (no false positives from user-controlled text, e.g. a username containing "from 1.2.3.4").
- Attack-input tests: a username like `root from 6.6.6.6 port 22` must not spoof the IP – assert the parser takes the *last* `from <ip> port <n>` or uses a strict structure.
- Add a fuzz target `agent/fuzz/fuzz_targets/security_<name>.rs` (no panics, bounded time).

## 4. Server side
- Ingest maps the event into the `security_events` table (partitioned); add sqlc query + migration if new columns are needed (tenant scoped, RLS).
- Enrichment (GeoIP local DB, known-IP tracking) is optional and must not call external services.
- Expose in Security dashboard table/filters; add i18n labels DE + EN.
- If alert-worthy → `sermon-add-alert-rule` (type `event`).

## Checklist
- [ ] Fixtures for all distros + OpenSSH variants, anonymized
- [ ] Positive, negative and spoofing tests; fuzz target
- [ ] Event schema, storage, UI labels DE + EN
- [ ] `sermon-security-review` passed
