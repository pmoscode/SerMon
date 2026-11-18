# CLAUDE.md – SerMon

SerMon ist ein schlankes, selbst gehostetes, mandantenfähiges Server-Monitoring (Netdata-ähnlich).
**Security hat oberste Priorität**, danach Einfachheit im Betrieb.

## Maßgebliche Dokumente – vor jeder Aufgabe lesen

- `docs/KONZEPT.md` – Architektur, Entscheidungen (§2), Security-Konzept (§18), Qualitätsregeln (§19), Repo-Struktur (§20).
  `KONZEPT.md` im Root ist eine Kopie: Änderungen am Konzept **in beiden Dateien** nachziehen
  (in `docs/` sind Links relativ zu `docs/`).
- `docs/IMPLEMENTIERUNGSPLAN.md` – Aufgaben mit IDs (`M0-01`, …), Abhängigkeiten, Abnahmekriterien, Status.
- `docs/adr/` – Architekturentscheidungen (ADR). Abweichungen vom Konzept nur mit neuem ADR (Skill `sermon-adr`).

## Stack

| Teil | Technik | Verzeichnis |
|---|---|---|
| Agent + root-Helper | Rust (statisch, musl; tokio, tonic, rustls) | `agent/` |
| Server | Go (stdlib `net/http`, connect-go, pgx, sqlc, goose) | `server/` |
| Web-UI | SvelteKit-SPA (Svelte 5 Runes, Tailwind 4, shadcn-svelte, uPlot, Paraglide) | `web/` |
| Protokoll | Protobuf / Connect-RPC, verwaltet mit `buf` | `proto/` |
| Speicher | PostgreSQL 17 + TimescaleDB (Stamm-, Event-, Metrikdaten), Loki (Logs) | – |
| Deployment | docker-compose | `deploy/` |

## Befehle

Alle Aufgaben laufen über `just` (siehe `just --list`). Ziele, die noch nicht umgesetzt sind, melden das und
nennen die Aufgaben-ID im Implementierungsplan.

- `just dev` – Entwicklungsumgebung starten
- `just gen` – Code aus `proto/` generieren (generierten Code nie von Hand ändern)
- `just lint` / `just fmt` – Lint bzw. Formatierung aller Teile
- `just test` – Unit-Tests; `just test-integration` – Integrationstests (Testcontainers)
- `just e2e` – End-to-End-Tests
- `just security` – Security-Scans (gosec, govulncheck, cargo-audit/deny, pnpm audit, gitleaks)

Voraussetzungen: `just`, `lefthook`, `gitleaks`, Rust (rustup; Version über `agent/rust-toolchain.toml` gepinnt),
`zig` + `cargo-zigbuild` (Cross-Build, ADR 0002), `cargo-deny`, Go, `golangci-lint` (v2), `govulncheck`,
Node + pnpm, `buf`, `shellcheck`, Docker.
`just setup` prüft, was fehlt.

Agent bauen: `just build-agent` (statische musl-Binaries für amd64 und arm64), `just smoke-agent` (Start im Container).
Server bauen: `just build-server` (statische Binary nach `server/bin/`), `just smoke-server` (Start, Health-Probes, SIGTERM).
Server-Konfiguration: Environment-Variablen `SERMON_*`, jeweils alternativ als Datei über `SERMON_*_FILE`.

## Arbeitsweise

- **Eine Aufgabe aus dem Plan = ein Branch (`<id>-<kurztitel>`, z. B. `m1-07-agent-ca`) = ein PR.**
  Status im Implementierungsplan im selben PR aktualisieren (☐ → ☑).
- **TDD ist Pflicht** für Logik (Skill `sermon-tdd`): erst der fehlschlagende Test, dann der Code, dann Refactoring.
- **Projekt-Skills nutzen** (`.claude/skills/`), z. B. `sermon-add-collector`, `sermon-add-alert-rule`,
  `sermon-proto-change`, `sermon-ui-component`.
- **Security-Review** (Skill `sermon-security-review`) bei allen 🔒-Aufgaben und bei jeder Änderung an Auth,
  Mandantentrennung, PKI, Agent/Helper, ausgehenden Requests, Secrets oder der Darstellung fremder Daten.
- **Definition of Done:** KONZEPT §19.4.
- Commits: Conventional Commits auf Englisch, Footer `Refs: <Aufgaben-ID>`.

## Konventionen

- **Sprache:** Code, Bezeichner, Code-Kommentare, Commit-Messages und Skills auf **Englisch**; Konzept, Plan, ADRs
  und Hilfetexte auf **Deutsch** (Hilfetexte zusätzlich Englisch unter `docs/help/en/`).
- **Clean Architecture:** Domäne ohne I/O; Abhängigkeiten zeigen nach innen; Ports und Adapter mit Fakes für Tests.
- **Starke Typen** statt `string`/`float64` (`NodeID`, `OrgID`, `Percent`, …).
- **Fehler explizit:** Go mit `fmt.Errorf("…: %w", err)` und `errors.Is/As`; Rust mit `thiserror` in Libraries und
  `anyhow` nur in Binaries; kein `unwrap()`/`expect()` in Produktionscode ohne begründenden Kommentar.
- **UI:** alle Texte über i18n (DE + EN); kein `{@html}` mit API-Daten.

## Security-Leitplanken (nie verletzen)

- Org- und Node-Identität beim Agent-Ingest **nur aus dem mTLS-Client-Zertifikat**, nie aus dem Payload.
- Jede Daten-Abfrage ist an einen `tenant.Scope` gebunden; standardmäßig wird verweigert.
- Keine freien Kommandos zum Agent oder Helper, kein `sh -c`, keine Raw-Queries von Usern.
- Secrets verschlüsselt (`secrets.Box`), nie loggen, nie an die UI zurückgeben.
- Ausgehende HTTP-Requests nur über `safehttp` (SSRF-Schutz).
- Keine neuen Abhängigkeiten ohne Prüfung (Pflege, Lizenz AGPL-kompatibel, `cargo-deny`/`govulncheck`).
- Keine Secrets, echten IPs oder Usernamen in Fixtures (192.0.2.0/24, 2001:db8::/32, `alice`, `bob`).
