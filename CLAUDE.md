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
- `just security` – Security-Scans (gosec, govulncheck, cargo-audit/deny, pnpm audit, betterleaks)

Voraussetzungen: `just`, `lefthook`, `betterleaks`, Rust (rustup; Version über `agent/rust-toolchain.toml` gepinnt),
`zig` + `cargo-zigbuild` (Cross-Build, ADR 0002), `cargo-deny`, Go, `golangci-lint` (v2), `govulncheck`,
Node + pnpm, `buf`, `shellcheck`, Docker.
`just setup` prüft, was fehlt.

Entwicklungsumgebung: `just dev` startet die Container aus `deploy/dev/compose.yaml` (TimescaleDB, Loki, Mailpit,
ntfy; nur auf 127.0.0.1), dazu `sermon-server` mit Hot-Reload (air) und den Vite-Dev-Server mit Proxy zum Server.
`just dev-infra`, `just dev-down`, `just dev-reset` (löscht Daten) und `just dev-logs` gibt es zusätzlich. Das
DB-Passwort wird beim ersten Start zufällig erzeugt (`deploy/dev/.secrets/`, git-ignoriert). Alternativ gibt es den
Devcontainer (`.devcontainer/`) mit allen Werkzeugen.

Agent bauen: `just build-agent` (statische musl-Binaries für amd64 und arm64), `just smoke-agent` (Start im Container).
Agent-Konfiguration: `/etc/sermon/agent.toml` (Schema und Beispiel in `agent/crates/sermon-agent/src/config.rs`);
Zeit nur über den Port `Clock`, Dateien unter `/proc`, `/sys` und `/etc` nur über `ProcRoot` (`sermon-core`).
Agent-Tests gegen einen Fake-Server: `sermon-testserver` (nur Dev-Abhängigkeit, PKI, TLS-Server, Fake-Services).
`just e2e-agent` lässt die Rust-Binary gegen den echten Server laufen (Testcontainers, auch in CI).
`just e2e-demo` baut alles, startet `deploy/e2e/compose.yaml` (Server, TimescaleDB, Caddy, Agent-Hosts mit systemd)
und spielt die M1-Demo mit Playwright durch (`KEEP=1` lässt die Umgebung laufen, `SKIP_BUILD=1` nutzt die letzten
Binaries).
`just simulate [nodes] [seed]` startet virtuelle Nodes gegen `just dev` (`sermon-agent simulate`, M1-34); der erste
Lauf braucht `SERMON_ENROLLMENT_TOKEN` mit genug Nutzungen, danach werden die Identitäten wiederverwendet.
Enrollment und Identitätsdateien: ADR 0010.
Server bauen: `just build-server` (statische Binary nach `server/bin/`), `just smoke-server` (Start, Health-Probes, SIGTERM).
Server-Konfiguration: Environment-Variablen `SERMON_*`, jeweils alternativ als Datei über `SERMON_*_FILE`.
Wichtige Variablen:
- `SERMON_DATABASE_URL` (Laufzeit als `sermon_app`)
- `SERMON_DATABASE_MIGRATE_URL` (Owner, Migrationen beim Start)
- `SERMON_ALLOWED_ORIGINS` (exakte Origins der UI; leer heißt: alle schreibenden API-Aufrufe werden abgelehnt)
- `SERMON_TRUSTED_PROXIES` (CIDRs, die `X-Forwarded-For` setzen dürfen)
- `SERMON_MASTER_KEYS_FILE` (Pflicht mit DB; anlegen mit `sermon-server init --master-keys-file PFAD`, nie ersetzen)
- `SERMON_AGENT_ADDR` + `SERMON_AGENT_HOSTNAMES` (mTLS-Agent-API; leer heißt aus; `just dev`: `127.0.0.1:8443`)
- `SERMON_METRICS_ADDR` (eigenes `/metrics` ohne Anmeldung, ADR 0011; leer heißt aus; `just dev`: `127.0.0.1:9464`)
- `SERMON_OUTBOUND_ALLOWLIST` (CIDRs interner Ziele für Kanäle/Webhooks über `safehttp`, ADR 0024; leer heißt nur
  öffentliche Adressen; `just dev`: `127.0.0.1/32`)
- `SERMON_AGENT_DOWNLOADS_DIR` (signierte Agent-Binaries für `/downloads`, ADR 0012; Release-Key wird beim Build
  über `SERMON_RELEASE_KEY` bzw. `packaging/release.pub` einkompiliert)

Erstinstallation: `sermon-server init` (Schlüssel, Migrationen, Agent-CA, Setup-Token). Solange die Einrichtung offen
ist, steht ein Setup-Token im Server-Log (`just dev`: Ausgabe von `sermon-server`); damit legt `SetupService` den
ersten Superadmin an.

Agent-Enrollment (ADR 0009): `sermon.agent.v1.EnrollmentService` läuft auf dem Web-Listener (gRPC über h2c hinter
Caddy), ohne Origin-Prüfung und Session, nur mit Enrollment-Token und Rate-Limit.
Web-API: Connect-Services unter `server/internal/transport/webapi/`, eingehängt über `webapi.Mount`
(Validierung, Origin-Prüfung, Security-Header). Die UI hängt auf `/` ohne Methode; Routen mit Methode wie `GET /`
kollidieren mit den Präfixen der API.
Web: `just build-web` (Build, Größenbudget, Kopie nach `server/web/dist` zum Einbetten), `just e2e` (Playwright gegen
die echte Server-Binary mit eingebetteter UI), `cd web && pnpm dev` (Vite-Dev-Server).

Protokoll: `proto/` mit buf (ADR 0004). `just gen` generiert Code für Go (`server/internal/gen`), TS
(`web/src/lib/api/gen`) und Rust (`agent/crates/sermon-transport/src/gen`, Server-Stubs für Tests in
`agent/crates/sermon-testserver/src/gen`), und zwar nur mit lokalen Plugins.
Generierter Code wird committet und nie von Hand geändert; die Zielordner leert `buf generate` bei jedem Lauf (keine
eigenen Dateien dort ablegen). Rust-Plugins installiert `just install-proto-plugins`.

CI: `.github/workflows/ci.yml` und `security.yml` rufen dieselben `just`-Befehle auf wie die lokale Entwicklung (ADR 0006).
Actions immer per Commit-SHA pinnen, die Version steht als Kommentar dahinter, und nur Versionen nehmen, die mindestens
3 Tage alt sind. Rechte minimal halten und jede zusätzliche Berechtigung kommentieren. Nach jeder Workflow-Änderung
`just lint-ci` (actionlint + zizmor) ausführen. Abhängigkeiten aktualisiert Renovate (`renovate.json`).

Datenbank (ADR 0007):
- Migrationen in `server/migrations/` (goose, eingebettet).
- Queries in `server/internal/adapters/postgres/queries/`, danach `just gen-sql`.
- **Jeder Datenzugriff läuft über `postgres.DB.InTenant(ctx, scope, …)`.** Neue Tabellen mit Mandantenbezug bekommen
  immer `ENABLE` + `FORCE ROW LEVEL SECURITY` und eine Policy auf `current_org_id()`, dazu Integrationstests für
  Isolation, Schreibschutz und „ohne Tenant nichts sichtbar“.
- Der Server verbindet sich als `sermon_app` (keine Sonderrechte); die Owner-Rolle nur für Migrationen.
- `just test-integration` startet die Testdatenbank per Testcontainers (Docker nötig).
- Metriken (ADR 0001): Port `app/metrics.Store` mit Contract-Tests (`metricstoretest`), Implementierungen
  `adapters/timescale` und `app/metrics/memstore` (Fake). `sermon_app` darf Metrik-Tabellen nicht lesen; gelesen wird
  über `metric_points_*` (`SECURITY DEFINER`). Ingest: `app/ingest` (10-s-Fenster, Ack erst nach dem Schreiben).

Web-Besonderheiten:
- SvelteKit 3: Die Konfiguration steht in `web/vite.config.ts` (es gibt keine `svelte.config.js`).
- Paraglide-Code (`web/src/lib/paraglide`) ist generiert und nicht committet; `pnpm run i18n` erzeugt ihn,
  `pnpm run check` und `prepare` tun das automatisch.
- Imports über `#lib/...` (package.json-Subpath-Imports) brauchen die Endung `.js`, z. B. `#lib/i18n/locale.svelte.js`.
- npm: Nur Releases, die mindestens 3 Tage alt sind, werden installiert. Schlägt `pnpm add` mit `NO_MATURE_MATCHING_VERSION`
  fehl, eine ältere Version wählen statt die Regel zu lockern.
- shadcn-svelte-Komponenten mit `pnpm dlx shadcn-svelte@1.8.1 add <name> --yes --no-deps-install` hinzufügen und
  danach die Abhängigkeiten exakt gepinnt per `pnpm add` installieren.

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
- **Autorisierung:** Jeder Use-Case fragt vor dem Handeln `authz.Policy.Allowed(principal, action, target)`; Listen
  holen ihren Scope über `authz.ScopeFor`. Neue Actions gehören in die Rollen-Matrix in `policy_test.go`; ein Test
  schlägt fehl, wenn eine Action dort fehlt.
- **Fehler explizit:** Go mit `fmt.Errorf("…: %w", err)` und `errors.Is/As`; Rust mit `thiserror` in Libraries und
  `anyhow` nur in Binaries; kein `unwrap()`/`expect()` in Produktionscode ohne begründenden Kommentar.
- **UI:** alle Texte über i18n (DE + EN); kein `{@html}` mit API-Daten.

## Security-Leitplanken (nie verletzen)

- Org- und Node-Identität beim Agent-Ingest **nur aus dem mTLS-Client-Zertifikat**, nie aus dem Payload.
- Jede Daten-Abfrage ist an einen `tenant.Scope` gebunden; standardmäßig wird verweigert.
- Keine freien Kommandos zum Agent oder Helper, kein `sh -c`, keine Raw-Queries von Usern.
- Secrets verschlüsselt (`secrets.Box`), nie loggen, nie an die UI zurückgeben.
- Ausgehende HTTP-Requests nur über `safehttp` (SSRF-Schutz).
- **UI lädt nichts von fremden Servern (D27):** keine CDNs, keine Google Fonts, kein Analytics. Libraries, Schriften
  (`@fontsource`), Icons und Bilder per npm installieren und mitbündeln; die CSP erlaubt nur `'self'`.
- Keine neuen Abhängigkeiten ohne Prüfung (Pflege, Lizenz AGPL-kompatibel, `cargo-deny`/`govulncheck`).
- Keine Secrets, echten IPs oder Usernamen in Fixtures (192.0.2.0/24, 2001:db8::/32, `alice`, `bob`).
