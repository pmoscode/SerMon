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
Module (ADR 0039): `agent/crates/sermon-modules` (Vertrag `Module`/`Instance`, Laufzeit, Schema-Teilmenge, Veto aus
`[local]` in `agent.toml`: `module_targets`, `module_hosts`, `module_ports`, `module_sockets`, `module_file_prefixes`,
`allow_docker_socket`); Module verbinden sich nur über `Veto::resolve` (Namen, Ports, Adressen) und `Veto::socket` vor
jeder Verbindung (im Lauf als `Run::socket`, auf einem Thread für blockierende Arbeit; `build` läuft ohnehin dort, V1-13)
und lesen Dateien nur über `Veto::open` (M5-33); Zugangsdaten behalten sie nur als `sermon_modules::Secret` (genullt); Tests mit Servern auf freien Ports nehmen
`Veto::default().any_port()`, Pfade in Tests `crate::TestDir`. Neue Module über den Skill `sermon-add-module`. Dateien des Hosts (`ProcRoot`) und den
Root-Helper (Port `Helper`) und systemd (Port `Systemd`: nur `systemctl show` mit festen Eigenschaften, M5-10) bekommen
Module über `Setup.host`; einzige Helper-Op eines Moduls ist `ReadSmart` (M5-18,
Veto `allow_smart` in `helper.toml`). Jedes Modul ist ein Cargo-Feature von `sermon-modules` (ab Werk alle an). Das Testmodul `simulate` gibt es nur mit
dem Feature `simulate` (`just simulate`, nie im Release). Module erreichen Dienste nur über
`sermon_modules::http::Client` (Veto, Weiterleitungen, Grenzen; M5-08); Discovery über `Module::discover` (M5-04).
Jedes Modul hat `schemas/<id>.json` (Formular, Prüfung) und `metrics/<id>.json` (Katalog seiner Metriken); `just gen`
kopiert beide in den Server (`server/internal/domain/module/schemas`, `server/internal/domain/metric/modules`). Die
Schema-Prüfung gibt es in Rust und Go (`server/internal/domain/module`) mit gemeinsamen Testfällen
(`agent/crates/sermon-modules/testdata/schema-vectors.json`). Server: `app/modules` (Einstellungen je Gruppe,
`ModuleService`), `module_settings`, `module_health`, `module_hints`, `module_secrets` (Zugangsdaten mit `secrets.Box`,
AAD `module.SecretContext`; zum Agent nur als `ModuleSecrets`, dort nur im Speicher, M5-05).
UI der Module (M5-07): Formular aus dem Schema (`web/src/lib/modules/schema.ts`), Texte je Feld in
`web/src/lib/modules/labels.ts` (`module_<id>_<feld>`, DE + EN; ein Test verlangt sie für jedes Schema).
Datenbank-Module (`postgres`, `mysql`) sprechen ihr Protokoll mit eigenen kleinen Clients (`src/<id>/wire.rs`, gefuzzt,
ohne Treiber-Crates); `just test-db-modules` prüft sie gegen echte Server in Containern (`FIXTURES=1` nimmt die
Fixtures neu auf; braucht `openssl` für die CA der MySQL-Server). Die Anmeldung von PostgreSQL ist ein reiner
Zustandsautomat (`postgres::wire::Login`, Einstellung `auth`, ab Werk nur SCRAM); TLS beider über `sql::Security`
(ohne `tls`: aus auf Loopback und am Socket, sonst `verify`; `ca_file`), das Passwort selbst nur über den Socket oder
geprüftes TLS (M5-33).
Agent-Tests gegen einen Fake-Server: `sermon-testserver` (nur Dev-Abhängigkeit, PKI, TLS-Server, Fake-Services).
`just e2e-agent` lässt die Rust-Binary gegen den echten Server laufen (Testcontainers, auch in CI).
`just e2e-demo` baut den Agent, startet die Produktions-Compose `deploy/compose/compose.yaml` mit dem Overlay
`deploy/e2e/compose.yaml` (Server-Image aus dem Quellcode, TimescaleDB, Caddy, Mailpit, ntfy, ein Slack-Mock unter
`hooks.slack.com`, Agent-Hosts mit systemd und OpenRC, einer mit echtem sshd, web-01 zusätzlich mit nginx, Apache auf
127.0.0.1:8080, Redis, PostgreSQL und MariaDB für die Module) und spielt die Demo mit Playwright durch
(`tests/demo`: erst die Installation, dann der SSH-Brute-Force bis zum Quittieren, die Module ab dem Vorschlag bis zum
Alert und der Kiosk, M5-32) (`KEEP=1` lässt die Umgebung laufen, `SKIP_BUILD=1` nutzt Agent,
Pakete und Secrets des letzten Laufs). Einzelne Projekte gegen eine laufende Umgebung: in `web/` mit `E2E_BASE_URL`,
`E2E_DIR`, `COMPOSE_FILE` und `COMPOSE_ENV_FILES` wie in `scripts/e2e-demo.sh` und
`pnpm exec playwright test --config playwright.demo.config.ts --project <name> --no-deps`.
Produktion: `deploy/compose/` (ADR 0028, Anleitung `docs/admin/installation.md`); jeder Container `read_only`, ohne
Capabilities und neue Privilegien, Agent-Port direkt am Server, die Owner-URL der Datenbank nur im einmaligen Service
`migrate`; `just lint-compose` prüft das (`check-compose.sh --hardened`, auch die Services der Profile). Erstinstallation mit
`docker compose run --rm migrate init …`.
Backup und Restore (V1-05, ADR 0042, Anleitung `docs/admin/backup.md`): `sermon-server backup --out DATEI|-` liest als
`sermon_backup` (read-only, BYPASSRLS, angelegt von `initdb/010-roles.sh`) einen Snapshot aller Tabellen außer
`postgres.BackupExclusions` (Metriken, Anomalie-Werte, Sessions, Leases; neue Tabellen sind automatisch drin) und
verschlüsselt mit age (`platform/backupfile`, gebunden an die Master-Keys, die nicht drin sind);
`sermon-server restore --in DATEI|- [--install-master-keys PFAD]` lädt als `sermon_owner` in einer Transaktion in eine
leere Datenbank; `sermon-server backup-keygen` erzeugt den Schlüssel. Exit-Code 3 = abgelehnt, nichts geändert. In
`deploy/compose` die einmaligen Services `backup` und `restore` (Profile, `docker compose run --rm -T backup > datei`).
`just e2e-restore` (V1-07, nightly mit `FULL=1`): nach der Demo Backup, neue Umgebung (Datenbank, Server-State, Loki),
Restore und die Prüfungen gegen die echte Compose (`SKIP_DEMO=1` gegen eine mit `KEEP=1` laufende Demo).
`just load-test [dauer] [nodes]` (V1-08, Anleitung `docs/admin/{de,en}/load-test.md`): baut wie die Demo (`e2e-demo.sh` mit
`PREPARE_ONLY=1`) und den Agent mit dem Simulator, startet die Produktions-Compose mit E2E-Overlay und
`deploy/load/compose.yaml` als Projekt `sermon-load` (eigene Ports 19443/19025/19464 und Netz 172.30.79.0/24, eine Demo
kann daneben laufen), richtet Admin, Gruppe `load` mit dem Modul `simulate` und ein Enrollment-Token ein, startet die
virtuellen Nodes in einem Container (Szenarien, `--log-rate` synthetische Journal-Zeilen) und misst mit
`scripts/load-report.sh` (`sample DIR DAUER [INTERVALL]`, `report DIR [--limit 'KEY.STAT<=V']… [--horizon TAGE]`:
`docker stats`, `/metrics`, SQL, Lesezugriffe der UI über die Web-API, Hochrechnung auf 7 Tage und die Aufbewahrung,
Grenzen mit Exit-Code 1). Ergebnis unter `deploy/load/.runs/<zeit>/report.md`; `KEEP=1`, `SKIP_BUILD=1` wie bei der
Demo. Dasselbe Skript misst den Testbetrieb gegen eine echte Installation (V1-18, `LOAD_URL`, `LOAD_METRICS_URL`,
`LOAD_EMAIL`/`LOAD_PASSWORD_FILE` oder `LOAD_COOKIE_FILE`, `LOAD_PSQL`, `LOAD_DOCKER=0`).
`just simulate [nodes] [--seed S] [--scenario …]` startet virtuelle Nodes gegen `just dev` (`sermon-agent simulate`,
M1-34); der erste Lauf braucht `SERMON_ENROLLMENT_TOKEN` mit genug Nutzungen, danach werden die Identitäten
wiederverwendet. Szenarien (M2-30, `simulate/scenario.rs`) für Tests der Alerts von Hand: `cpu-high`, `disk-filling`,
`ssh-bruteforce`, `flapping`, `offline`; `--dump 2h` schreibt die Werte ohne Server als JSON-Zeilen, damit prüft
`just e2e-agent` jedes Szenario im Zeitraffer gegen seine Standardregel.
Enrollment und Identitätsdateien: ADR 0010.
Server bauen: `just build-server` (statische Binary nach `server/bin/`), `just smoke-server` (Start, Health-Probes, SIGTERM).
Server-Konfiguration: Environment-Variablen `SERMON_*`, jeweils alternativ als Datei über `SERMON_*_FILE`.
Wichtige Variablen:
- `SERMON_DATABASE_URL` (Laufzeit als `sermon_app`)
- `SERMON_DATABASE_MIGRATE_URL` (Owner `sermon_owner`, Migrationen beim Start; in `deploy/compose` nur im einmaligen
  Service `migrate` = `sermon-server migrate`, nie im Server, M2-36)
- `SERMON_ALLOWED_ORIGINS` (exakte Origins der UI; leer heißt: alle schreibenden API-Aufrufe werden abgelehnt)
- `SERMON_TRUSTED_PROXIES` (CIDRs, die `X-Forwarded-For` setzen dürfen)
- `SERMON_MASTER_KEYS_FILE` (Pflicht mit DB; anlegen mit `sermon-server init --master-keys-file PFAD`, nie ersetzen)
- `SERMON_AGENT_ADDR` + `SERMON_AGENT_HOSTNAMES` (mTLS-Agent-API; leer heißt aus; `just dev`: `127.0.0.1:8443`)
- `SERMON_METRICS_ADDR` (eigenes `/metrics` ohne Anmeldung, ADR 0011; leer heißt aus; `just dev`: `127.0.0.1:9464`)
- `SERMON_DATABASE_SIZE_LIMIT` (z. B. `50GiB`; Self-Monitoring warnt Superadmins ab 80 %, ADR 0027; leer heißt keine
  Prüfung)
- `SERMON_ORG_DELETION_GRACE` (Frist zwischen „Zum Löschen vormerken“ und dem Löschen einer Org, ADR 0029; Standard
  `720h`, mindestens `168h`; der Löschjob prüft stündlich)
- `SERMON_NODE_DELETION_GRACE` (Frist zwischen „Node löschen“ und dem Löschen seiner Daten, ADR 0041; Standard `168h`,
  mindestens `24h`; der Löschjob prüft stündlich)
- `SERMON_ACCOUNT_DELETION_GRACE` (Konten ohne Mitgliedschaft werden danach gelöscht, V1-17; Standard `720h`,
  mindestens `24h`, Superadmins nie; stündlich, `app/account`)
- `SERMON_AUDIT_RETENTION` (Aufbewahrung der Audit-Logs der Orgs und der Instanz, V1-17; Standard `17520h`, `2160h` bis
  `87600h`; gekürzte Ketten bleiben über einen Eintrag `audit.prune` prüfbar)
- `SERMON_LOKI_URL` (Loki für Logs, ADR 0031; feste URL ohne Zugangsdaten, eigener Client ohne Proxy und Redirects;
  leer heißt: keine Logs; `deploy/compose`: `http://loki:3100` im internen Netz)
- `SERMON_OUTBOUND_ALLOWLIST` (CIDRs interner Ziele für Kanäle/Webhooks über `safehttp`, ADR 0024; leer heißt nur
  öffentliche Adressen; `just dev`: `127.0.0.1/32`)
- `SERMON_AGENT_DOWNLOADS_DIR` (signierte Agent-Binaries für `/downloads`, ADR 0012; Release-Key wird beim Build
  über `SERMON_RELEASE_KEY` bzw. `packaging/release.pub` einkompiliert)
- `SERMON_REPO_DIR` (signierte Paket-Repos aus `scripts/build-repos.sh` für `/repo` und `install.sh --repo`,
  ADR 0036)
- Nur für `backup`/`restore` (ADR 0042; der Server liest sie nie): `SERMON_DATABASE_BACKUP_URL` (Rolle
  `sermon_backup`), `SERMON_BACKUP_RECIPIENTS` (öffentliche age-Schlüssel, eine Zeile je Schlüssel),
  `SERMON_BACKUP_IDENTITY` (geheimer age-Schlüssel, nur beim Restore); dazu `SERMON_MASTER_KEYS_FILE` und beim Restore
  `SERMON_DATABASE_MIGRATE_URL`

Erstinstallation: `sermon-server init` (Schlüssel, Migrationen, Agent-CA, Setup-Token). Solange die Einrichtung offen
ist, steht ein Setup-Token im Server-Log (`just dev`: Ausgabe von `sermon-server`); damit legt `SetupService` den
ersten Superadmin an.

Agent-Enrollment (ADR 0009): `sermon.agent.v1.EnrollmentService` läuft auf dem Web-Listener (gRPC über h2c hinter
Caddy), ohne Origin-Prüfung und Session, nur mit Enrollment-Token und Rate-Limit.
Web-API: Connect-Services unter `server/internal/transport/webapi/`, eingehängt über `webapi.Mount`
(Validierung, Origin-Prüfung, Security-Header). Der `KioskService` (M5-26) hängt nicht am `Authenticator`, sondern am
`KioskAuthenticator` (Header `Authorization: Kiosk smk_…`, keine Session); jede neue RPC muss mit einem Kiosk-Token
dieselbe Antwort geben wie ohne (`kiosk_integration_test.go`). Die UI hängt auf `/` ohne Methode; Routen mit Methode wie `GET /`
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

Releases (V1-03, ADR 0043, Anleitung `docs/release.md`): `release-please.yml` hält den Release-PR (Konfiguration
`release-please-config.json`, Version in `.release-please-manifest.json`; jede Version erst `X.Y.Z-rc.N`, ein Release
über `release-as`), sein Merge startet `release.yml` auf dem Tag (`workflow_dispatch`); `release-pr.yml` verlangt den
Abschnitt „Upgrade“ (`scripts/check-release-notes.sh`). Nur der Job `sign` (Environment `release`) kennt die Schlüssel
(`SERMON_RELEASE_SIGNING_KEY`, `SERMON_PKG_GPG_KEY`, `SERMON_PKG_APK_KEY`); Dateien zwischen Jobs nur über
`scripts/release-artifacts.sh` (Siegel). Bausteine, die lokal laufen: `just release-agent VERSION` (Binaries mit den
Public Keys aus `packaging/`), `scripts/release-package.sh` (Arbeit des Signier-Jobs), `just release-dry-run` (alles
mit Testschlüsseln, `IMAGE=1` mit Image), `just verify-release VERSION` (`scripts/verify-release.sh`, nur Public Keys).
Die echten Schlüssel erzeugt `scripts/release-keys.sh generate` (einmal, Product Owner). Einzige Ausnahme vom Pinnen
per SHA: die Generatoren von `slsa-github-generator` per Tag (`.github/zizmor.yml`).

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
- Kiosk-Anzeige `/kiosk` (M5-27) außerhalb von `(app)`, ohne Session: eigener Client (`#lib/kiosk/api.js`,
  `credentials: 'omit'`); `KioskApp` importiert bewusst die gemeinsamen Module aller Seiten, sonst teilt der Bundler die
  gemeinsamen Chunks anders auf und jede Seite wird größer.
- JavaScript-Budget (KONZEPT §17): unter 200 KiB pro Seite, Flotte und Node-Seite mit 10 KiB Luft (`budget.e2e.ts`).
  Was erst beim Öffnen gebraucht wird (Popover wie `HelpTip`, Formulare), mit `{#await import(…)}` bzw. `import()` bei
  Bedarf laden. Die vollen Hilfetexte sind im Build JSON-Dateien (`scripts/help.js`), kein JavaScript; die Summe aller
  Bundles prüft `pnpm run check:size` (520 KiB, gemessen unter Linux ~0,3 % mehr als unter macOS).
- Hilfetexte und Handbuch (V1-10): `docs/help/{de,en}/…` (Format und Prüfungen in `docs/help/README.md`),
  Benutzerhandbuch `docs/user/{de,en}/README.md` verlinkt sie statt sie zu wiederholen. `pnpm run lint:help` verlangt,
  dass jedes Thema auch gezeigt wird (UI, Link oder ein vom Server gebildeter Name `metrics/`, `modules/`,
  `channels/`), gleiche Schlüssel in `web/messages/{de,en}.json` und ein Handbuch ohne tote Links, das jede Seite
  nennt; `server/cmd/sermon-server/help_test.go` verlangt Texte für alle Metriken, Module, Kanaltypen und Regeln.
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
- Datenschutz (V1-11): `docs/privacy/{de,en}/README.md` ist die Vorlage für Betreiber (Verarbeitungsverzeichnis,
  TOMs, Löschfristen, Auftragsverarbeitung). Wer gespeicherte personenbezogene Daten, Fristen oder Schutzmaßnahmen
  ändert, passt sie im selben PR an; die Fristen prüft `server/cmd/sermon-server/privacy_test.go` gegen die Konstanten.
- Admin-Doku (V1-09): Index `docs/admin/README.md`, je Sprache `docs/admin/{de,en}/` (`hardening.md`,
  `operations.md`, `troubleshooting.md`, Referenz `configuration.md`). Wer eine `SERMON_*`-Variable des Servers, einen
  Schlüssel in `[local]` der `agent.toml` oder in `helper.toml` oder eine Option von `install.sh` hinzufügt, dokumentiert
  sie im selben PR in beiden Sprachen; `server/cmd/sermon-server/admin_docs_test.go` verlangt das und prüft
  Voreinstellungen, Fristen und Links gegen den Code.

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
