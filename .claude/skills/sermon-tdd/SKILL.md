---
name: sermon-tdd
description: Test-driven workflow (Red → Green → Refactor) for SerMon code in Rust (agent), Go (server) and SvelteKit (web). Use whenever implementing or changing logic — parsers, collectors, alert engine, anomaly detection, authz, use-cases, UI logic — or fixing a bug (write the reproducing test first).
---

# SerMon TDD workflow

Follow KONZEPT.md §19. Never write production logic before a failing test exists.

## Loop

1. **Clarify behaviour** – write down 3–8 concrete examples (input → expected output), including edge cases and error cases. For bugs: the reproducing case first.
2. **Red** – write ONE test for the simplest example. Run it, confirm it fails *for the right reason* (assertion, not compile error caused by typos).
3. **Green** – minimal code to pass. No speculative generality.
4. **Refactor** – remove duplication, improve names, extract functions; tests stay green. Refactor commits are separate from behaviour commits.
5. Repeat with the next example. Finish with `just lint && just test`.

## Where tests go and how to run them

| Part | Location | Run |
|---|---|---|
| Rust agent | `#[cfg(test)] mod tests` next to code; cross-crate in `agent/crates/<crate>/tests/` | `cargo test -p <crate>` (in `agent/`) |
| Rust fixtures | `agent/fixtures/<distro>/proc/...`, `agent/fixtures/logs/<distro>/*.log` | inject via `ProcRoot` / `&str` input |
| Go server | `*_test.go` same package; table-driven `tests := []struct{name string; ...}` | `go test ./internal/<pkg>/...` (in `server/`) |
| Go integration | `//go:build integration`, Testcontainers-go (PG/TimescaleDB, Loki) | `just test-integration` |
| Web | `*.test.ts` next to component / module (Vitest + Testing Library) | `pnpm -C web test` |
| E2E | `web/tests/e2e/*.spec.ts` (Playwright + axe) | `just e2e` |

## Rules

- **Pure core, thin shell:** put logic in pure functions / domain types; test I/O adapters separately with integration tests. If a test needs mocks of 3+ things, the design is wrong – extract.
- **Fakes over mocks:** every port (`Clock`, `Collector`, `Notifier`, repositories) has a hand-written fake in the same package/crate (`fake.go`, `testing.rs`). Never use real time – inject `Clock`.
- **Parsers** (proc, log lines, security events, rule YAML): add real-world fixtures for *every* target distro (Debian, Ubuntu, RHEL/Rocky, Alpine) and add/extend a fuzz target (`agent/fuzz/`, `go test -fuzz`).
- **Property tests** for invariants (`proptest` / `rapid`): e.g. "CPU percentages sum to 100 ± ε", "rate never negative across counter wrap".
- **Snapshot tests** (`insta`) only for large structured outputs; review snapshot diffs consciously.
- **Security tests are mandatory** for anything touching authz or tenant data: add a case to the role-matrix test and the tenant-isolation test (user of Org A must get `NotFound`/`PermissionDenied` for Org B).
- Test names describe behaviour: `parses_failed_password_for_invalid_user_ipv6`, `TestAlertEngine_GoesPendingThenCritical_AfterForDuration`.
- No `sleep` in tests. No network except Testcontainers. Deterministic seeds.
- Coverage targets: domain/parsers ≥ 90 %, overall ≥ 80 % – don't game it; add meaningful assertions.

## Done when

- All new behaviour is covered by tests that failed before the change.
- `just lint` and `just test` are green.
- Definition of Done in KONZEPT.md §19.4 is satisfied.
