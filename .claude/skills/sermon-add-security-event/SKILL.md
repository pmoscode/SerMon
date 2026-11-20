---
name: sermon-add-security-event
description: Add or extend a security event parser in the SerMon agent that turns journald/auth.log lines into structured events (ssh logins, sudo, account changes, OOM, reboot). Use when asked to detect a new security-relevant log pattern or to fix parsing for a distro/OpenSSH version.
---

# Add a security event parser

Reference: KONZEPT.md §11. Crate: `agent/crates/sermon-security/`.

## 1. Collect real samples first
- Gather **real** log lines for every target distro and relevant software versions (e.g. OpenSSH < 9.8 logs as `sshd`, ≥ 9.8 as `sshd-session`), both journald JSON and classic `/var/log/auth.log` / `/var/log/secure` formats.
- Include IPv4, IPv6, `invalid user`, usernames with spaces/unicode, unusual ports, PAM variants – and **forged lines** of an unprivileged user (`logger -t <ident> …`).
- Extend `agent/fixtures/logs/capture/capture.py` (systemd containers per distro, `capture.sh [image…]`) and regenerate; it writes `agent/fixtures/logs/<distro>/journal-*.jsonl`. Hostname, machine-id and test passwords are scrubbed; use 192.0.2.0/24, 203.0.113.0/24, 2001:db8::/32, `alice`, `bob`, `carol`.

## 2. Define the event
- Event type `<domain>.<subject>.<result>` (e.g. `ssh.login.failed`). Add to the proto enum/schema only via `sermon-proto-change` if new.
- Fields: always `ts` (from log, not receive time), plus typed fields (`user: String`, `ip: IpAddr`, `port: u16`, `method: AuthMethod`). Never pass the raw line as an event field (logs are shipped separately and redacted).

## 3. TDD the parser (see `sermon-tdd`)
- Crate `agent/crates/sermon-security`: `Parser::parse(&LogLine, &mut Vec<SecurityEvent>)` dispatches on `SYSLOG_IDENTIFIER`, then the message. New kinds go into `EventKind`, `name()`, the proto (`SecurityEvent` oneof in `proto/sermon/agent/v1/agent.proto`) and `agent/crates/sermon-agent/src/security.rs` (`to_proto`).
- **Trust (ADR 0023):** only privileged senders (`_UID` 0 or `_CAP_EFFECTIVE` ≠ 0) count for sshd/sudo/useradd/userdel; kernel events only with `_TRANSPORT=kernel`. A new identifier needs the same guard and a forged-line test.
- Use `regex` crate (linear time) with anchored patterns compiled once (`LazyLock`). Prefer explicit patterns per message type over one giant regex.
- Table-driven tests over all fixture lines: expected event or `None`. Lines that must **not** match are just as important (no false positives from user-controlled text, e.g. a username containing "from 1.2.3.4").
- Attack-input tests: a username like `root from 6.6.6.6 port 22` must not spoof the IP – assert the parser takes the *last* `from <ip> port <n>` or uses a strict structure.
- The fuzz target `agent/fuzz/fuzz_targets/security_parser.rs` covers every identifier; add a new one to its list (`just fuzz-agent`).
- Fixture expectations live in `agent/crates/sermon-security/tests/fixtures.rs` (one list per file, all distros).
- Regenerate the golden batch (`SERMON_UPDATE_GOLDEN=1 cargo test -p sermon-agent security`); the server validates it with protovalidate (`TestSchema_AgentSecurityEventsAreValid`). This also catches missing `regex` features: crate tests get unicode features from dev-dependencies, the agent build does not.

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
