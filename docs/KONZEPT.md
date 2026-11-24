# SerMon – Konzept

> **SerMon** (Server Monitor) ist ein schlankes, selbst gehostetes und mandantenfähiges Monitoring für Linux-Server.
> Netdata dient als Vorbild, der Funktionsumfang ist aber bewusst kleiner. Oberstes Gebot ist **Security**, gefolgt von
> Einfachheit im Betrieb.

|        |              |
|--------|--------------|
| Status | Entwurf v0.1 |
| Datum  | 2026-11-18   |
| Lizenz | AGPL-3.0     |

---

## Inhalt

1. [Ziele und Nicht-Ziele](#1-ziele-und-nicht-ziele)
2. [Entscheidungslog](#2-entscheidungslog)
3. [Architektur](#3-architektur)
4. [Komponenten](#4-komponenten)
5. [Kommunikation und Protokoll](#5-kommunikation-und-protokoll)
6. [Zero-Config-Installation und Enrollment](#6-zero-config-installation-und-enrollment)
7. [Mandanten, User und Rechte](#7-mandanten-user-und-rechte)
8. [Metriken](#8-metriken)
9. [Live-Ansicht („top“)](#9-live-ansicht-top)
10. [Logs](#10-logs)
11. [Security-Events](#11-security-events)
12. [Alerting](#12-alerting)
13. [Anomalie-Erkennung](#13-anomalie-erkennung)
14. [Paket-Updates und Auto-Update](#14-paket-updates-und-auto-update)
15. [Integrationen und Module](#15-integrationen-und-module)
16. [Dashboards](#16-dashboards)
17. [UI/UX und Hilfe](#17-uiux-und-hilfe)
18. [Security-Konzept](#18-security-konzept)
19. [Qualität: Clean Code, TDD und Prozess](#19-qualität-clean-code-tdd-und-prozess)
20. [Repository-Struktur](#20-repository-struktur)
21. [CI/CD, Release und Supply Chain](#21-cicd-release-und-supply-chain)
22. [Deployment und Betrieb](#22-deployment-und-betrieb)
23. [Roadmap](#23-roadmap)
24. [Offene Punkte und Risiken](#24-offene-punkte-und-risiken)
25. [Agent-Skills (Claude Code)](#25-agent-skills-claude-code)

---

## 1. Ziele und Nicht-Ziele

### Ziele

- **Zero-Config-Agent:** Ein Einzeiler mit Token, danach liefert der Agent Metriken, ohne dass etwas konfiguriert werden
  muss.
- **Zentraler Server** mit moderner Web-UI, Mandanten (Orgs), Gruppen und Rollen. User sehen nur „ihre“ Nodes.
- **Basis-Metriken** (CPU, Memory, Disk-IO, Netzwerk, Load, Uptime) mit Detailansichten und einer **Live-Ansicht** wie
  `top`.
- **Logs:** systemd-journal, klassische Log-Dateien und Docker-Logs.
- **Alerting** mit sinnvollen Standardregeln nach dem Vorbild von Netdata, darunter **Security-Events** (SSH-Logins) und
  **Update-/Reboot-Status**.
- **Statistische Anomalie-Erkennung**, die erklärbar ist und ohne Training auskommt.
- **Policy-gesteuerte Paket-Updates** (apt/dnf/apk) mit Wartungsfenstern.
- **Optionale Integrationen** (nginx, PostgreSQL, Docker usw.), die im Agent eingebaut sind und zentral aktiviert
  werden.
- **Security by Design:** mTLS, Least Privilege, Mandantentrennung, sichere Supply Chain.

### Nicht-Ziele (v1)

- Keine Skalierung über ca. 50 Nodes pro Installation; die Architektur verbaut das aber nicht.
- Kein APM/Tracing und kein vollwertiger Ersatz für Prometheus/Grafana.
- Keine Remote-Shell und keine frei definierbaren Remote-Kommandos, auch keine Prozess-Kills aus der Live-Ansicht.
- Kein Windows- oder macOS-Agent.
- Keine Hochverfügbarkeit des Servers. Ziel ist ein Single-Instance-Betrieb mit sauberem Backup und Restore.
- Kein Fremdcode im Agent (keine externen Plugins; WASM ist frühestens in v2 denkbar).

---

## 2. Entscheidungslog

Ergebnis der Konzeptrunde. Für jede Entscheidung gibt es ein ADR unter `docs/adr/`; die Zuordnung steht in der
[ADR-Übersicht](adr/README.md).

| #   | Thema          | Entscheidung                                                                                 | Begründung                                                                                            |
|-----|----------------|----------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------|
| D1  | Agent          | **Rust** (statisch, musl)                                                                    | Speichersicherheit, kleiner Footprint, eine Binary für alle Distros                                   |
| D2  | Server         | **Go**                                                                                       | Einfache Nebenläufigkeit, starkes Netzwerk-Ökosystem, eine statische Binary                           |
| D3  | Frontend       | **SvelteKit (SPA, adapter-static)**, Tailwind, shadcn-svelte, uPlot                          | Kleine Bundles, wenig Boilerplate, schnelle Live-Updates; wird in die Server-Binary eingebettet       |
| D4  | Topologie      | **Zentraler Server, Agents pushen, mit lokalem Puffer**                                      | Auf den Clients sind keine eingehenden Ports nötig; bei Verbindungsverlust gehen keine Daten verloren |
| D5  | Betriebsmodell | **Self-hosted, mandantenfähig** (Org als Konzept von Anfang an)                              | Später SaaS-fähig, ohne das Datenmodell umzubauen                                                     |
| D6  | Skalierung     | **< 50 Nodes** als Zielgröße                                                                 | Hält den Betrieb einfach                                                                              |
| D7  | Zielsysteme    | Debian/Ubuntu, RHEL/Fedora/Rocky/Alma, Alpine, **amd64 + arm64**                             |                                                                                                       |
| D8  | Installation   | **Einzeiler mit Enrollment-Token** (`curl … \| sh`), optional signierte Pakete (.deb/.rpm/.apk) | Zero-Config                                                                                           |
| D9  | Agent-Rechte   | **Privilegien getrennt:** Agent als User `sermon`, minimaler root-Helper nur für Updates     | Least Privilege                                                                                       |
| D10 | Auth           | **Lokal (Argon2id) + TOTP + Passkeys/WebAuthn, OIDC/SSO**                                    |                                                                                                       |
| D11 | Rechte         | **Org → Node-Gruppen → Rollen** (Viewer / Operator / Admin)                                  |                                                                                                       |
| D12 | Retention      | **Gestaffelt:** 1 s / 15 min (Ringpuffer im Server), 10 s / 24 h, 1 min / 30 Tage, 1 h / 1 Jahr; Logs 14 Tage; Events 1 Jahr | konfigurierbar; DB-Auflösung 10 s nach Spike M0-09 (ADR 0001) |
| D13 | Storage        | **PostgreSQL 17 + TimescaleDB** (Stamm-, Event- und Metrikdaten), **Loki** (Logs)            | Eine DB statt fünf Komponenten; Downsampling und Retention nativ; Metrik-Speicher austauschbar ([ADR 0001](adr/0001-zeitreihen-speicher-timescaledb.md)) |
| D14 | Alert-Kanäle   | E-Mail, Webhook, Slack/Teams/Discord/Mattermost, Telegram/ntfy/Gotify/Pushover               |                                                                                                       |
| D15 | Updates        | **Policy-basiert** pro Gruppe (report / security-auto / all-auto), Wartungsfenster           |                                                                                                       |
| D16 | Anomalien      | **Statistisch, serverseitig** (robuster Z-Score, saisonale Baselines)                        | Erklärbar, ohne Training, gut testbar                                                                 |
| D17 | Logs           | journald, klassische Dateien, Docker, beliebige Dateien per Glob                             |                                                                                                       |
| D18 | Module         | **Im Agent eingebaut, standardmäßig aus**, zentral konfiguriert; dazu ein Prometheus-Scraper | Kein Fremdcode                                                                                        |
| D19 | Live-Ansicht   | **Nur lesen**                                                                                | Kleinere Angriffsfläche                                                                               |
| D20 | UI-Sprache     | **DE + EN**, i18n von Anfang an; Kontexthilfe                                                |                                                                                                       |
| D21 | Lizenz         | **AGPL-3.0**                                                                                 |                                                                                                       |
| D22 | Alert-Regeln   | **UI + YAML-Import/Export**                                                                  | GitOps-fähig                                                                                          |
| D23 | Deployment     | **docker-compose**                                                                           |                                                                                                       |
| D24 | Reihenfolge    | **Metriken → Alerts → Logs → Updates**                                                       |                                                                                                       |
| D25 | Repo/CI        | **GitHub-Monorepo + GitHub Actions**                                                         |                                                                                                       |
| D26 | API            | **Connect-RPC/Protobuf** für Agent ↔ Server (gRPC) und Web ↔ Server (Connect)                | Ein Schema, typisiert von Ende zu Ende                                                                |
| D27 | UI-Ressourcen  | **Alles lokal ausgeliefert:** keine CDNs, keine externen Schriften, Skripte, Styles, Bilder oder Icons | Datenschutz (keine IP-Weitergabe an Dritte, DSGVO), Air-Gap-fähig, strikte CSP, keine Supply-Chain über fremde Server |
| D28 | Mitglieder     | **Einladungslink:** einmalig, 72 h gültig, nur als Hash gespeichert; **in Produktion nur per Mail** an die Adresse (SMTP Pflicht), im Entwicklungsmodus ohne SMTP zum Kopieren | Adresse bewiesen, kein Konto-Squatting, keine User-Enumeration, Admin kennt keine Passwörter ([ADR 0008](adr/0008-einladungen.md), [ADR 0022](adr/0022-betriebsmodus-und-einladungen-per-mail.md)) |
| D29 | Orgs löschen   | **Erst sperren, nach Frist endgültig löschen:** Sperre beendet Logins und Ingest sofort; nach der Frist (Standard 30 Tage) werden Stamm-, Event-, Metrik- und Loki-Daten gelöscht; das Audit-Log ist vorher exportierbar; Details in [ADR 0029](adr/0029-organisationen-verwalten-sperren-loeschen.md) | Schutz vor Fehlbedienung und Missbrauch, Daten bleiben bis zum Löschen nachvollziehbar |
| D30 | Enrollment     | **Über den öffentlichen HTTPS-Endpunkt**, danach mTLS auf dem Agent-Port | Neue Agents kennen die Agent-CA noch nicht; der mTLS-Port bleibt streng ([ADR 0009](adr/0009-enrollment-ueber-web-endpunkt.md)) |

---

## 3. Architektur

```
 ┌──────────────────────── Linux-Node (n×) ────────────────────────┐
 │                                                                 │
 │  ┌──────────────────────┐   Unix-Socket   ┌──────────────────┐  │
 │  │ sermon-agent (Rust)  │ ──────────────▶ │ sermon-helper    │  │
 │  │ user: sermon         │  (feste Ops,    │ (root, minimal)  │  │
 │  │ • Collectors /proc   │   SO_PEERCRED)  │ • apt/dnf/apk    │  │
 │  │ • journald / Files   │                 │ • reboot         │  │
 │  │ • Security-Events    │                 └──────────────────┘  │
 │  │ • Module (opt.)      │                                       │
 │  │ • WAL-Puffer (disk)  │                                       │
 │  └──────────┬───────────┘                                       │
 └─────────────┼───────────────────────────────────────────────────┘
               │ ausgehend: gRPC über HTTP/2 + mTLS (bidirektionaler Stream)
               ▼
 ┌──────────────────────── Server (docker-compose) ────────────────┐
 │  ┌───────────┐   ┌──────────────────────────────────────────┐   │
 │  │  Caddy    │──▶│ sermon-server (Go)                       │   │
 │  │ TLS / ACME│   │ • Agent-API (gRPC, mTLS, interne CA)     │   │
 │  └───────────┘   │ • Web-API (Connect) + eingebettete SPA   │   │
 │                  │ • Ingest → TimescaleDB / Loki            │   │
 │                  │ • Alert-Engine, Anomalie-Worker          │   │
 │                  │ • Notifier, Update-Orchestrierung        │   │
 │                  └───┬───────────────────────────┬──────────┘   │
 │                      ▼                           ▼              │
 │              ┌─────────────────────┐       ┌─────────────┐      │
 │              │ PostgreSQL 17       │       │    Loki     │      │
 │              │ + TimescaleDB       │       │    Logs     │      │
 │              │ Stamm-, Event- und  │       │             │      │
 │              │ Metrikdaten         │       │             │      │
 │              └─────────────────────┘       └─────────────┘      │
 └─────────────────────────────────────────────────────────────────┘
               ▲
               │ HTTPS (Browser)
          ┌────┴────┐
          │  Web-UI │  SvelteKit-SPA
          └─────────┘
```

**Grundprinzipien**

- Der Agent baut **immer** die Verbindung auf. Der Server erreicht Nodes ausschließlich über den bestehenden Stream.
- **Nur der Server** spricht mit PostgreSQL und Loki. Diese Systeme sind nie von außen erreichbar und
  haben ein eigenes internes Netz in docker-compose.
- Der Server prüft Rechte und Mandanten bei **jeder** Anfrage. Die Datenbanken selbst kennen keine User.
- **Org-ID und Node-ID** stammen beim Ingest ausschließlich aus dem Client-Zertifikat, niemals aus dem Payload.

---

## 4. Komponenten

### 4.1 Agent (`sermon-agent`, Rust)

| Aspekt         | Festlegung                                                                                                                                                  |
|----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Build          | `x86_64-unknown-linux-musl`, `aarch64-unknown-linux-musl`; statisch gelinkt                                                                                 |
| Runtime        | `tokio`; gRPC-Client mit `tonic` + `rustls` (kein OpenSSL)                                                                                                  |
| Collectors     | Eigene Parser für `/proc` und `/sys`. Das Wurzelverzeichnis ist injizierbar (`ProcRoot`), dadurch sind Tests mit Fixtures möglich                           |
| Journal        | Kindprozess `journalctl -o json --all -f --after-cursor=…`; den Cursor speichert der Agent selbst, sobald der Eintrag im WAL liegt ([ADR 0013](adr/0013-log-quelle-journald.md)). Kein Linken gegen libsystemd, damit die musl-Binary statisch bleibt |
| Dateien        | Tailing mit inotify, Rotationserkennung (inode/size) und Offsets persistiert                                                                                |
| Puffer         | Write-Ahead-Log auf Disk (`/var/lib/sermon-agent/wal`), segmentiert, Obergrenze z. B. 24 h oder 200 MB (was zuerst erreicht ist); FIFO-Verwerfen mit Zähler |
| Config         | Fast nichts lokal (`/etc/sermon/agent.toml`: Server-URL, Pfade, lokale Verbote). Alles Weitere kommt **versioniert vom Server**                             |
| Ressourcenziel | < 30 MB RSS und < 1 % eines Kerns bei 1-s-Sammlung                                                                                                          |
| Init-Systeme   | systemd-Unit (gehärtet, siehe §18.4) und OpenRC-Script für Alpine                                                                                           |

**Interne Struktur (Traits/Ports):**
`Collector` (Metriken), `LogSource`, `EventParser` (Security-Events), `Transport`, `Buffer`, `Clock`. Jeder Port hat
eine Fake-Implementierung für Tests.

### 4.2 Root-Helper (`sermon-helper`, Rust)

- Ein eigener, **winziger** Prozess (Ziel: < 1 000 LoC, in CI geprüft) mit eigener systemd-Unit, der als root läuft;
  eigenes Paket `sermon-helper` ([ADR 0035](adr/0035-root-helper.md)).
- Er lauscht auf `/run/sermon/helper.sock` (Rechte `root:sermon 0660`, unter systemd per Socket-Aktivierung) und prüft
  per `SO_PEERCRED`, dass der Aufrufer die UID `sermon` hat. Eine Anfrage pro Verbindung, Protobuf mit vorangestellter
  Länge (`sermon.helper.v1`).
- Er nimmt **nur ein festes Enum an Operationen** an: `GetStatus`, `ListUpdates{refresh}`,
  `ApplyUpdates{run_id, scope: security|all}`, `Reboot{run_id, reason}`, `SelfUpdate` (ADR 0036) und
  `RunResult{run_id}`. Freie Kommandos, Paketnamen oder Pfade gibt es nicht.
- Er führt nur feste Befehle aus und gibt ihre Ausgabe begrenzt zurück; **geparst wird im Agent** ohne Rechte.
- Ein Lauf ist eine eigene Unit (`systemd-run`, unter OpenRC eine eigene Session): Er übersteht den Neustart von
  Agent und Helper, wenn ein Update deren Pakete erneuert. Das Ergebnis holt der Agent mit `RunResult` ab.
- **Lokale Hoheit:** `/etc/sermon/helper.toml` mit `allow_remote_updates` (ab Werk an), `allow_remote_reboot` (aus)
  und `allow_self_update` (an). Diese Werte kann der Server **nicht** überschreiben; ohne Helper gibt es keine
  Fernsteuerung.
- Jede Operation wird lokal (journald) und zentral (Audit-Log) protokolliert, auch abgelehnte.

### 4.3 Server (`sermon-server`, Go)

| Aspekt          | Festlegung                                                                                                                                                                                                                            |
|-----------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Architektur     | Hexagonal / Clean Architecture: `domain` (rein, ohne Abhängigkeiten) → `app` (Use-Cases) → `adapters` (Postgres/Timescale, Loki, Notify, OIDC) → `transport` (gRPC/Connect)                                                                       |
| HTTP/RPC        | `net/http` (stdlib) + `connectrpc.com/connect`                                                                                                                                                                                        |
| DB              | PostgreSQL 17 + TimescaleDB-Extension, `pgx`, `sqlc` (typisierte Queries), `goose` (Migrationen); Row-Level-Security als zweite Verteidigungslinie. Migrationen laufen als Owner-Rolle, der Server läuft als `sermon_app` ohne Sonderrechte und verweigert privilegierte Rollen (ADR 0007) |
| Metriken        | Port `MetricStore` mit Implementierung auf TimescaleDB-Hypertables (selbe PG-Instanz). Schreiben gebündelt per `COPY`, Lesen **nur über die typisierte Query-Schicht**. Die Implementierung ist austauschbar (z. B. VictoriaMetrics bei Wachstum, siehe ADR 0001) |
| Logs            | Loki-Push-API mit `X-Scope-OrgID = <org-uuid>`, eine feste URL des Betreibers (`SERMON_LOKI_URL`), eigener Client ohne Proxy und Redirects ([ADR 0031](adr/0031-logs-in-loki.md))                                                     |
| Background-Jobs | Alert-Engine, Anomalie-Worker, Notifier, Update-Orchestrator, Zertifikatsrotation, Retention. Implementiert als In-Process-Worker mit Leases in PG (`SELECT … FOR UPDATE SKIP LOCKED`), sodass später mehrere Instanzen möglich wären |
| Konfiguration   | Environment-Variablen plus optionale YAML-Datei; Secrets über `*_FILE` (Docker-Secrets)                                                                                                                                               |
| Observability   | Eigene `/metrics` (Prometheus), strukturierte Logs (`slog`, JSON), Health-Endpunkte `/healthz` und `/readyz`                                                                                                                          |

### 4.4 Web-UI (`web/`, SvelteKit)

- SvelteKit 3 + Svelte 5 (Runes), `adapter-static` als SPA; das Ergebnis wird per `embed.FS` in die Go-Binary
  eingebettet.
- Tailwind CSS 4, shadcn-svelte (Bits UI) für die Komponenten, **uPlot** für Zeitreihen (schnell, Canvas-basiert).
- Connect-Web-Client, aus `proto/` generiert (`buf generate`).
- i18n mit **Paraglide JS** (typsicher, Tree-Shaking).
- Tests: Vitest + Testing Library (Komponenten), Playwright (E2E), axe-core (Accessibility).

### 4.5 Datenhaltung

| Daten                                                                               | System                                   | Retention                                        |
|-------------------------------------------------------------------------------------|------------------------------------------|--------------------------------------------------|
| Orgs, User, Gruppen, Rollen, Nodes, Zertifikate, Regeln, Policies, Kanäle, Sessions | PostgreSQL                               | dauerhaft                                        |
| Alert-Instanzen und -Historie, Security-Events, Update-Läufe, Audit-Log             | PostgreSQL (partitioniert nach Monat)    | 1 Jahr (Audit: konfigurierbar, Standard 2 Jahre) |
| Metriken 1 s                                                                        | Ringpuffer im Server-Speicher (nicht persistent)             | 15 min                                           |
| Metriken 10 s (avg/min/max je Fenster)                                              | TimescaleDB-Hypertable `metrics_raw`                         | 24 h                                             |
| Metriken 1 min                                                                      | Continuous Aggregate `metrics_1m`                            | 30 Tage                                          |
| Metriken 1 h                                                                        | Continuous Aggregate `metrics_1h` (hierarchisch auf `metrics_1m`) | 1 Jahr                                      |
| Logs                                                                                | Loki (Multi-Tenant, Compactor-Retention)                     | 14 Tage                                          |
| Zustellprotokoll der Benachrichtigungen (mit den Feldern der Alerts)                | PostgreSQL `notification_deliveries`     | 30 Tage                                          |

**Metrik-Schema in TimescaleDB** (Details in [ADR 0001](adr/0001-zeitreihen-speicher-timescaledb.md)):

- **Schmales Schema mit Integer-IDs:**
  - `series(id int, org_id, node_id, metric text, labels jsonb)`, eindeutig über `(org_id, node_id, metric, labels)`
  - `metrics_raw(ts timestamptz, series_id int, avg, min, max double precision)`: ein Eintrag je Serie und 10-s-Fenster,
    gebildet vom Server aus den 1-s-Werten
- **Hypertable** mit **1-h-Chunks** und Index **`(series_id, ts DESC)`**: Ohne diesen Index dauert ein Chart unter Last
  720 ms statt 16 ms. **Kompression nach 2 h** (`segmentby = series_id`, `orderby = ts`).
- **Continuous Aggregates** speichern `avg`, `min`, `max` und `last` je Bucket. Jede Stufe hat eine eigene **Retention-Policy**, die Aktualisierung läuft per Refresh-Policy.
- **Counter rechnet der Agent in Raten pro Sekunde um** (§8), damit in den Aggregaten keine Window-Funktionen nötig sind.
- **Ingest:** Der Server fasst die 1-s-Werte jedes Nodes zu 10-s-Fenstern zusammen. Ein Fenster ist abgeschlossen,
  sobald Daten eines späteren Fensters eintreffen; abgeschlossene Fenster schreibt er alle 2 s per `COPY`, zusammen
  mit der höchsten Batch-Seq des Nodes (`nodes.ingest_seq`) in einer Transaktion. Erst danach bestätigt er die Batches
  (`Ack`). Offene Fenster werden nie geschrieben; endet die Session, sendet der Agent ihre Batches erneut. So wird jeder
  Batch genau einmal gespeichert. Unbekannte Serien werden vorab in `series` angelegt (mit Cache).
- **Ringpuffer im Server-Speicher** (letzte 15 min, 1 s) für Live-Ansicht, Live-Charts und kurze Alert-Fenster.
- **Werte möglichst ganzzahlig** in Basiseinheiten (Bytes/s, Pakete/s, Anteile in ppm). Die Kompression erreicht
  dann 3,1 Byte/Messwert statt 10 Byte bei Gleitkommawerten mit voller Genauigkeit (§8).
- **Mandantentrennung bei Metriken:** TimescaleDB unterstützt RLS weder auf komprimierten Hypertables noch auf
  Continuous Aggregates. `sermon_app` darf die Metrik-Tabellen deshalb nicht direkt lesen. Gelesen wird über eine
  `SECURITY DEFINER`-Funktion, deren Eigentümer keine Sonderrechte hat und die nur Serien zurückgibt, die über die
  RLS-geschützte Tabelle `series` sichtbar sind (ADR 0001, 0007).
- **PostgreSQL-Einstellungen** für die Schreiblast: `wal_compression = lz4`, `max_wal_size = 8GB`,
  `checkpoint_timeout = 15min`.
- **Spike M0-09:** Ergebnisse und Messwerte in ADR 0001.

**Mengengerüst (50 Nodes):**

- Etwa 400 Serien pro Node ergeben 20 000 Samples/s. Gemessen bei 1 s in der DB: 61 Byte/Sample unkomprimiert,
  10 Byte (Gleitkomma) bzw. 3,1 Byte (ganzzahlig) komprimiert, WAL ~13–15 GB/h (~320 GB/Tag).
- Mit **10 s in der DB** (2 000 Zeilen/s mit avg/min/max): ca. 173 Mio. Zeilen pro Tag, unter 5 GB für 24 h und
  WAL ca. 30–40 GB/Tag. Eine SSD wird empfohlen.
- `metrics_1m` braucht für 30 Tage ca. 5–15 GB, `metrics_1h` für 1 Jahr < 5 GB.
- Logs schwanken stark; geschätzt werden 0,5–5 GB/Tag.

---

## 5. Kommunikation und Protokoll

- **Schema:** `proto/sermon/agent/v1/*.proto` und `proto/sermon/web/v1/*.proto`, verwaltet mit `buf` (Lint und
  Breaking-Change-Check in CI); dazu `proto/sermon/helper/v1` zwischen Agent und Root-Helper (§4.2).
- **Agent ↔ Server:** Ein langlebiger **bidirektionaler gRPC-Stream** `AgentService.Session`.
    - Agent → Server: `Hello{agent_version, host_facts, config_version}` (`host_facts` mit `security_source`: Journal,
      Datei verifiziert/unverifiziert oder keine), `Heartbeat`,
      `RotateCertificate{csr}` (neues Schlüsselpaar, §6 „Schlüsselrotation“), `Batch{seq, …}` mit genau einem
      Inhalt: `MetricBatch`, `SecurityEventBatch`, `LogBatch` (M3-01), `UpdateReport` (verfügbare Updates, Reboot nötig,
      Rechte des Helpers) und `CommandResult{run_id, phase, exit_code, output}` (M4-05); dazu `ConfigApplied` (Antwort
      auf `ConfigUpdate`) und `ProcessSnapshot` bzw. `LiveViewRefused{reason}` (Antwort auf `StartLiveView`, M3-13).
      `CommandResult` ist gepuffert wie alle Batches: Das Ergebnis übersteht den Neustart des Agents, auch den durch
      sein eigenes Update; es kommt einmal je Phase (gestartet, erfolgreich, fehlgeschlagen, abgelehnt …).
    - Server → Agent: `TrustBundle{roots}` (zu Beginn jeder Session), `Ack{seq}`, `Throttle{min_interval}`,
      `CertificateIssued`, `RotateNow` (Rotation auf Abruf), `ConfigUpdate{version, config}` (Log-Auswahl und
      Redaction-Regeln, [ADR 0030](adr/0030-konfiguration-vom-server.md)),
      `StartLiveView{interval, hide_cmdline}` (eine Lease: ohne Erneuerung endet die Live-Ansicht nach 60 s, ebenso mit
      der Session), `StopLiveView`, `RefreshUpdateInfo`, `RunUpdate{run_id, scope}`, `RebootNode{run_id, reason}` und
      `SelfUpdate{run_id, version}` (M4-05; der Reboot nach einem Update ist ein eigener Befehl). Agents ohne diese
      Befehle ignorieren sie wie jede unbekannte Nachricht.
- **Enrollment:** `EnrollmentService.Enroll{token, csr, host_facts}` ist ein einzelner Aufruf über den öffentlichen
  HTTPS-Endpunkt, nicht über den mTLS-Port ([ADR 0009](adr/0009-enrollment-ueber-web-endpunkt.md)).
- **Batching:** Metriken werden jede Sekunde gesendet, Logs alle 1 s oder bei 256 KB (je nachdem, was zuerst eintritt).
  Komprimiert wird mit gzip auf gRPC-Ebene (zstd bräuchte eine C-Bibliothek im statischen Agent und folgt nur bei
  Bedarf).
- **Zuverlässigkeit:** Jeder Batch hat eine monoton steigende Sequenznummer, der Server bestätigt (`Ack{seq}`). Der
  Agent löscht erst nach dem Ack aus dem WAL (*at-least-once*) und hat höchstens 64 unbestätigte Batches unterwegs.
  Die Seq steigt auch über Neustarts und den Verlust des WAL hinweg (ein neues WAL beginnt bei der Zeit in µs), kann
  aber Lücken haben (verworfene Daten). Der Server dedupliziert deshalb über „Seq ≤ höchste gespeicherte Seq des
  Nodes“.
- **Backpressure und Quotas:** Der Server limitiert pro Node (Samples/s, Log-Bytes/s, Anzahl Serien). Bei Überschreitung
  schickt er `Throttle`, der Agent drosselt und meldet das.
- **Versionierung:** Protokollversion im `Hello`. Der Server unterstützt die aktuelle und die vorherige Minor-Version
  des Agents.
- **Keepalive:** HTTP/2-Pings alle 15 s. Nach 60 s ohne Heartbeat gilt der Node als „offline“ (Alert-Quelle).

---

## 6. Zero-Config-Installation und Enrollment

### Ablauf

1. Ein Admin bzw. Operator klickt in der UI auf **„Node hinzufügen“** und wählt die Ziel-Gruppe. Der Server erzeugt
   einen **Enrollment-Token**:
    - zufällig (256 Bit); in der DB wird nur der **SHA-256-Hash** gespeichert
    - **gültig 1 h** (konfigurierbar), Nutzung **einmalig** oder N-mal (für Massen-Rollouts, z. B. mit Ansible)
    - gebunden an Org und Node-Gruppe
2. Die UI zeigt den Befehl an. Das Token steht in einer Variable, nie in der Kommandozeile (Prozessliste); das
   Leerzeichen am Anfang hält es in bash aus der History:
   ```bash
    export SERMON_ENROLLMENT_TOKEN=sme_XXXXXXXX
   curl -fsSL https://sermon.example.com/install.sh | sudo --preserve-env=SERMON_ENROLLMENT_TOKEN sh -s -- \
     --server https://sermon.example.com
   ```
   Ohne öffentlich vertrauenswürdiges Zertifikat (selbstsigniert, eigene CA) trägt der Admin den Fingerprint ein, und
   der Befehl prüft `install.sh` vor dem Ausführen mit `sha256sum` gegen die Prüfsumme, die der Browser vom Server
   bekommen hat ([ADR 0012](adr/0012-agent-installation-und-signaturen.md), Nachtrag M1-37):
   ```bash
    export SERMON_ENROLLMENT_TOKEN=sme_XXXXXXXX
   curl -fsSLk https://sermon.example.com/install.sh -o sermon-install.sh
   echo '<sha256>  sermon-install.sh' | sha256sum -c - && sudo --preserve-env=SERMON_ENROLLMENT_TOKEN \
     sh sermon-install.sh --server https://sermon.example.com --ca-fingerprint sha256:AB:CD:…
   ```
   Daneben gibt es einen Link **„Skript erst ansehen“** und die Alternative per Paket-Repo
   (`apt install sermon-agent && sermon-agent enroll …`).
3. `install.sh` (POSIX-sh, ShellCheck-geprüft):
    - erkennt Distro, Architektur und Init-System
    - lädt die Agent- und Helper-Binary samt **Signatur** und prüft sie gegen den **im Skript fest eingebetteten Public
      Key** (minisign/cosign). Die Signatur muss auch den Dateinamen und **genau die Version des Servers** nennen
      (kein Rollback auf ein älteres signiertes Release). Schlägt die Prüfung fehl, bricht das Skript ab.
    - legt den User `sermon` an und setzt die Gruppen (`systemd-journal`, `adm`)
    - installiert die Units (systemd/OpenRC) und ruft `sermon-agent enroll` auf
4. `enroll`:
    - erzeugt **lokal** ein Schlüsselpaar (ECDSA P-256), das den Host nie verlässt, und daraus eine CSR
    - sendet Token, CSR und Host-Fakten (Hostname, OS, Kernel, CPU, RAM, machine-id) über TLS an den öffentlichen
      HTTPS-Endpunkt des Servers ([ADR 0009](adr/0009-enrollment-ueber-web-endpunkt.md)). Das Server-Zertifikat wird
      gegen die System-CA **oder** den gepinnten Fingerprint geprüft.
    - Der Server prüft den Token, legt den Node an und signiert die CSR mit der **internen Agent-CA**:
        - Subject/SAN: `URI:spiffe://sermon/org/<org-id>/node/<node-id>`
        - Laufzeit **30 Tage**; automatische Rotation nach 2/3 der Laufzeit über den bestehenden Stream
    - Der Agent speichert Zertifikat und Schlüssel (`0600`, Owner `sermon`) sowie die CA-Kette und startet.
5. Nach wenigen Sekunden erscheint der Node in der UI. Die Onboarding-Ansicht zeigt live: „Verbindung hergestellt ✓ –
   erste Metriken ✓“.

### Widerruf und Lebenszyklus

- Node löschen oder deaktivieren → Zertifikats-Serial auf die **Denylist** (wird bei jedem Verbindungsaufbau geprüft).
  Wegen der kurzen Laufzeiten bleibt die Liste klein.
- Re-Enrollment (z. B. nach Neuinstallation) erkennt die `machine-id` und bietet in der UI „bestehenden Node übernehmen“
  an. Das geschieht nie automatisch.
- Deinstallation: `sermon-agent uninstall` bzw. Paket entfernen; optional mit Abmeldung beim Server.

### Schlüsselrotation (angelehnt an Komodo, verschärft)

- **Automatisch nach Zeit:** Agent-Zertifikate gelten 30 Tage. Nach 2/3 der Laufzeit erzeugt der Agent ein **neues
  Schlüsselpaar**, und der Server stellt ein neues Zertifikat aus. Nach einer kurzen Übergangszeit kommt das alte auf
  die Denylist. Es gibt keinen Opt-out pro Node. Nach jeder Rotation endet die Session, und der Agent verbindet sich
  sofort mit dem neuen Zertifikat; das in der alten Session ausgestellte gilt dabei als Nachfolger, nicht als zweite
  Session.
- **Rotation auf Abruf** (z. B. nach Verdacht auf Kompromittierung oder einem Leak im Backup):
  - „Schlüssel jetzt rotieren“ für einen Node, eine Gruppe oder alle Nodes einer Org.
  - Nur für Operator und Admin, mit Step-up-2FA und Eintrag im Audit-Log.
  - Das alte Zertifikat wird **sofort** gesperrt, auch für schon offene Sessions.
  - Ist ein Node gerade offline, wird sein altes Zertifikat trotzdem sofort gesperrt; der Node muss danach neu
    enrollt werden.
  - **Nur widerrufen** (`revoke_only`): Bei Verdacht auf einen **gestohlenen** Schlüssel könnte der gerade verbundene
    Agent der Dieb sein und bekäme bei einer Rotation das neue Zertifikat. Dann werden alle Zertifikate sofort
    widerrufen und die Sessions beendet, ohne Rotation; die echten Hosts werden neu enrollt.
- **In der UI** (M2-23): Die Node-Seite zeigt eine Sperre sofort im Kopf; der Reiter „Identität“ zeigt Sperrgrund,
  letzte Adresse und ein offenes Übernahmefenster. Operatoren der Gruppe können dort rotieren, widerrufen,
  **entsperren** (das alte Zertifikat gilt wieder – bei Verdacht auf Diebstahl stattdessen widerrufen) und eine
  **Übernahme erlauben** (eine Stunde: Das nächste Enrollment mit derselben `machine-id` und einem Token **derselben
  Gruppe** übernimmt den Node mit ID und Historie, seine alten Zertifikate werden widerrufen,
  [ADR 0009](adr/0009-enrollment-ueber-web-endpunkt.md)). Alles mit Step-up und Audit.
- **Rotation auf Server-Seite:**
  - Das Server-Zertifikat der Agent-API ist kurzlebig und wird automatisch erneuert.
  - CA-Wechsel per **Trust-Bundle**: Der Server verteilt über den Stream ein Bundle mit alter **und** neuer CA. Danach
    stellt er Agent-Zertifikate nur noch mit der neuen CA aus.
  - Die alte CA wird erst entfernt, wenn kein gültiges Zertifikat der alten CA mehr existiert.
  - Fortschritt und Abschluss sind in der UI sichtbar.
- **Erkennung paralleler Nutzung derselben Identität:** Ein Angreifer mit gestohlenem Schlüssel könnte zuerst rotieren
  und den echten Agent aussperren. Dagegen helfen:
  - **höchstens eine aktive Session pro Node**;
  - ein **Security-Alert**, wenn eine zweite Session mit derselben Identität aufgebaut wird oder die Rotation des echten
    Agents scheitert, weil sein Zertifikat bereits ersetzt wurde;
  - ein Hinweis, wenn eine Rotation von einer anderen Quell-IP kommt als die bisherige Session.

  In diesen Fällen wird die Identität vorsorglich gesperrt, bis ein Admin den Node freigibt oder neu enrollt.

---

## 7. Mandanten, User und Rechte

### Datenmodell (vereinfacht)

```
Organization 1─n NodeGroup 1─n Node
Organization 1─n Membership n─1 User
Membership   1─n RoleBinding (NodeGroup | "*", Role)
Organization 1─n AlertRule, NotificationChannel, NotificationPolicy, UpdatePolicy, ModuleConfig
User         1─n Credential (password | totp | webauthn) , Session, ApiToken (später)
```

- Ein User kann Mitglied **mehrerer Orgs** sein und wechselt zwischen ihnen (Org-Switcher).
- Ein **Superadmin** (Instanz-Ebene) verwaltet Orgs, sieht aber **keine** Daten einer Org, solange er nicht selbst
  Mitglied ist. Seine Seite „Organisationen“ zeigt nur Zahlen (Mitglieder, Nodes, geschätzter Speicher), Status und
  **Kontingente** ([ADR 0029](adr/0029-organisationen-verwalten-sperren-loeschen.md)): höchstens so viele Nodes und
  Mitglieder (mit offenen Einladungen), eine Obergrenze der Retention, Samples pro Sekunde und Serien pro Node statt der
  Werte der Instanz. An der Grenze wird Neues abgelehnt (Enrollment, Einladung, Annahme, neue Serien, Drosselung),
  Bestehendes läuft weiter. Namen von Orgs sind eindeutig ohne Unterschied von Groß- und Kleinschreibung.
  Ein Superadmin **sperrt** eine Org oder **merkt sie zum Löschen vor** (D29, M2-33), jeweils mit Step-up und dem
  abgeschriebenen Namen: Sitzungen, Logins, Agents, Enrollment-Tokens und Einladungen der Org werden sofort abgelehnt,
  Alerts ruhen, die Zertifikate bleiben. Nach der Frist (`SERMON_ORG_DELETION_GRACE`, Standard 30 Tage) löscht der
  Server die Org samt Daten und Konten ohne weitere Mitgliedschaft; vorher lässt sich die Sperre aufheben oder das
  Audit-Log mit Hash-Kette herunterladen.
- Ein Node gehört genau einer Gruppe. Zusätzlich gibt es **Tags** (frei, z. B. `env=prod`, `role=db`) für Filter und
  Regel-Matching, aber nicht für Rechte. Sie setzen Admins der Gruppe des Nodes mit Step-up und Audit-Eintrag;
  höchstens 16 Paare, Schlüssel aus Kleinbuchstaben, Ziffern, `_` und `-`, Werte aus Buchstaben, Ziffern und
  `_ . : / -` (M2-29). Regel-Selektoren, Notification-Policies und Silences können Tags verlangen.

### Rollen

| Recht                                       | Viewer | Operator | Admin (Org) |
|---------------------------------------------|:------:|:--------:|:-----------:|
| Nodes, Metriken, Dashboards ansehen         |   ✓   |    ✓    |     ✓      |
| Logs und Security-Events ansehen            |  ✓¹   |    ✓    |     ✓      |
| Live-Ansicht                                |   ✓   |    ✓    |     ✓      |
| Alerts quittieren und stummschalten         |   –    |    ✓    |     ✓      |
| Updates manuell auslösen, Reboot            |   –    |    ✓    |     ✓      |
| Enrollment-Token erzeugen                   |   –    |    ✓    |     ✓      |
| Tags von Nodes setzen                       |   –    |    –     |     ✓      |
| Regeln, Kanäle, Policies, Module bearbeiten |   –    |    –     |     ✓      |
| User, Gruppen, Rollen verwalten             |   –    |    –     |     ✓      |
| Audit-Log ansehen                           |   –    |    –     |     ✓      |

¹ Pro Org abschaltbar („Viewer dürfen keine Logs sehen“), weil Logs personenbezogene Daten enthalten.

Rollen werden **pro Node-Gruppe** vergeben (oder für `*` = alle Gruppen der Org). Die Prüfung erfolgt ausschließlich
serverseitig in der `app`-Schicht über eine zentrale `authz.Policy` (testbar als Tabelle).

### Authentifizierung

- **Lokal:** Passwort mit Argon2id (memory 64 MiB, t=3, p=1; Parameter konfigurierbar), mindestens 12 Zeichen, Prüfung
  gegen eine Liste häufiger Passwörter (offline, mitgeliefert).
- **2FA:** TOTP (RFC 6238) und **WebAuthn/Passkeys** (`go-webauthn`). Pro Org kann 2FA verpflichtend gemacht werden. Bei
  Einrichtung werden Recovery-Codes erzeugt (gehasht gespeichert).
  - **TOTP** (SHA-1, 30 s, 6 Stellen, ±1 Schritt): Seed versiegelt mit dem Master-Key und an den User gebunden; jeder
    Zeitschritt und jeder der 10 Recovery-Codes (80 Bit) gilt einmal. Einrichten, Abschalten und neue Codes verlangen
    einen Step-up.
  - **Login:** Nach dem Passwort entsteht eine Session, die höchstens 5 min auf den zweiten Faktor wartet und sonst
    nichts darf. Fehlversuche beim Code zählen zur Kontosperre; das Passwort setzt den Zähler dann nicht zurück.
  - **Org-Pflicht:** Admins (mit eigenem zweiten Faktor) schalten sie per `SecurityPolicyService`. Mitglieder ohne
    zweiten Faktor erreichen in dieser Org nur die Einrichtung (`FAILED_PRECONDITION` mit `SecondFactorSetupRequired`).
  - **Passkeys** (M3-23, [ADR 0034](adr/0034-passkeys-mit-webauthn.md)): RP-ID ist der Host der ersten Origin,
    `attestation: none`, höchstens 10 pro User, Hinzufügen und Löschen mit Step-up und Audit. Ein Passkey ist ein
    zweiter Faktor (Org-Pflicht), die Anmeldung ohne Passwort verlangt User Verification und zählt als zwei Faktoren;
    dazu zweiter Faktor nach dem Passwort und Step-up. Zeremonien 5 min, einmal, an Session oder Browser-Cookie
    gebunden; ein rückwärts laufender Zähler wird abgewiesen (synchronisierte Passkeys mit 0 nicht); Fehlversuche
    zählen zur Kontosperre.
    In der UI (M3-24): Karte „Passkeys“ in der Kontosicherheit (hinzufügen, umbenennen, löschen), „Mit Passkey
    anmelden“ und Conditional UI im E-Mail-Feld, „Passkey statt Code“ nach dem Passwort, Bestätigung im
    Step-up-Dialog. Mit zweitem Faktor bestätigt das Passwort keinen Step-up mehr.
- **OIDC/SSO:** `coreos/go-oidc` mit Authorization Code + PKCE. **Gruppen-Claim-Mapping** (OIDC-Gruppe →
  Org/NodeGroup/Rolle) ist konfigurierbar; Just-in-Time-Provisioning ist optional.
    - Seit M3-20 ([ADR 0033](adr/0033-anmeldung-ueber-oidc.md)): ein Provider pro Instanz (`SERMON_OIDC_ISSUER`,
      `…_CLIENT_ID`, `…_CLIENT_SECRET_FILE`, `…_SCOPES`, `…_NAME`, `…_MFA`). Discovery, Schlüssel und Token über
      `safehttp`; `state`, `nonce` und Verifier in einem einmal nutzbaren Flow (10 min), gebunden an ein Cookie des
      Browsers. Identität über `iss` + `sub`, Verknüpfen nur angemeldet mit Step-up.
    - Die 2FA-Pflicht einer Org gilt als erfüllt, wenn `amr` einen zweiten Faktor nennt oder der Betreiber dem
      Provider vertraut; sonst folgt der lokale zweite Faktor. Step-up auch über eine frische Anmeldung beim Provider
      (`max_age=0`, `auth_time` geprüft).
    - Seit M3-21: Admins einer Org ordnen Provider-Gruppen (Claim `groups`) einer Node-Gruppe oder `*` und einer Rolle
      zu. Jede Anmeldung gleicht die Rollen aus Zuordnungen ab (Herkunft `oidc` an der Bindung); von Hand Vergebenes
      bleibt, der letzte org-weite Admin auch, gesperrte Orgs sind ausgenommen. JIT (abschaltbar mit
      `SERMON_OIDC_JIT=false`) legt Konto und Mitgliedschaft an, nur mit bestätigter E-Mail, die noch keinem Konto
      gehört, und im Rahmen des Kontingents. Beitreten tut nur ein bei dieser Anmeldung angelegtes Konto; wen ein
      Admin entfernt, der bleibt draußen (M3-28).
    - Seit M3-28: Der Step-up beim Provider gilt für User mit eigenem zweiten Faktor nur mit einem zweiten Faktor
      dort (`amr` oder `trusted`). Konten ohne Passwort nutzen Passkeys statt TOTP.
    - Seit M3-29: Start und Rückkehr (OIDC) bzw. Start und Antwort (Passkeys) haben getrennte Rate-Limits, offene
      Flows und Zeremonien höchstens je 10 000. Das Lösen einer Identität und das Löschen eines Passkeys beenden die
      übrigen Sessions des Users.
    - Seit M3-22: Knopf auf der Anmeldeseite, Verknüpfen und Lösen unter „Kontosicherheit“ (Lösen nur, wenn ein
      Passwort oder eine weitere Identität bleibt), Step-up beim Provider im Bestätigungsdialog, Zuordnungen im Reiter
      „SSO-Gruppen“ der Organisation. Rollen aus Zuordnungen tragen in der Mitgliederliste das Zeichen „SSO“.
- **Sessions:** serverseitig in PG, Cookie `__Host-sermon_session` (Secure, HttpOnly, SameSite=Lax). Idle-Timeout 30
  min, absolut 12 h. **Step-up-Authentifizierung** (erneute 2FA) für sensible Aktionen wie Updates, Reboot oder
  Rechteänderungen.
- **Step-up** gilt 15 min. Rechteänderungen (Rollen vergeben oder entziehen, Mitglied entfernen, Gruppe löschen) und
  Enrollment-Tokens anlegen (ein Token lässt Maschinen der Org beitreten) verlangen ihn.
    - Die vollständige Liste (M3-25) steht als Tabelle `stepUpRequired` in den Tests der Web-API
      (`server/internal/transport/webapi/stepup_cases_test.go`): Rechte und Einladungen, OIDC-Zuordnungen, eigene
      Faktoren (TOTP, Passkeys, OIDC lösen), Org-Pflicht für 2FA, Enrollment-Tokens, Identitäten und Tags von Nodes,
      Agent-CA, Alert-Regeln, Silences und Wartungsfenster, Kanäle und Richtlinien, Log-Einstellungen und Redaction,
      der Viewer-Logs-Schalter, das Ausblenden von Befehlszeilen, Security-Alerts quittieren, alle Änderungen an
      Organisationen und der Export ihres Audit-Logs. Jede andere schreibende RPC steht mit Grund in `stepUpExempt`
      (z. B. Zugriff entziehen, nur umbenennen, Testnachricht senden); eine neue RPC ohne Eintrag lässt die Tests
      scheitern.
  Erneuert wird er mit dem zweiten Faktor; ohne eingerichtete 2FA mit dem Passwort (`AuthService.ConfirmPassword`).
  Fehlversuche zählen wie beim Login zur Kontosperre. Fehlt der Step-up, antwortet der Server mit `FAILED_PRECONDITION`
  und dem Detail `StepUpRequired`; die UI fragt dann nach und wiederholt die Aktion.
  Jede Org behält mindestens einen Admin für alle Gruppen, den letzten kann niemand entfernen.
- **Org-Wechsel** stellt ein neues Session-Cookie aus. Absoluter Timeout und Step-up-Zeitpunkt bleiben erhalten:
  Ein Wechsel verlängert keine Session und gilt nicht als erneute Anmeldung.
- **Brute-Force-Schutz:** Rate-Limit pro IP und pro Account, progressive Verzögerung, Benachrichtigung bei verdächtigen
  Logins.

---

## 8. Metriken

**Namensschema** angelehnt an die *OpenTelemetry System Semantic Conventions*, umgesetzt in Prometheus-Notation:
`sermon_<bereich>_<name>_<einheit>`, z. B. `sermon_cpu_utilization_ratio{mode="iowait"}` oder
`sermon_network_receive_bytes_per_second{interface="eth0"}`.
Pflicht-Labels: `org_id` und `node_id` (setzt der Server, nicht der Agent). Fachliche Labels: `cpu`, `device`,
`interface`, `mountpoint`, `mode`. **Cardinality-Limits:** max. 2 000 aktive Serien pro Node, gezählt über alle Sessions
(aktiv = Daten in den letzten 48 h, ein Reconnect bringt kein neues Budget), und 250 000 pro Org; Interfaces wie `veth*`
und Loop-Devices werden standardmäßig gefiltert.

### 8.1 Basis-Metriken (immer aktiv, Intervall 1 s)

| Bereich                                  | Metriken                                                                                                                                                    | Quelle                                                                                              |
|------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------|
| **CPU**                                  | Auslastung gesamt und pro Kern nach Modus (user, system, iowait, irq, softirq, steal, nice, idle); Context-Switches; Interrupts; CPU-Pressure (PSI)         | `/proc/stat`, `/proc/pressure/cpu`                                                                  |
| **Load**                                 | load1/5/15, normiert auf die Kernzahl; laufende und blockierte Prozesse                                                                                     | `/proc/loadavg`, `/proc/stat`                                                                       |
| **Memory**                               | total, used, free, available, buffers, cached, shared, slab; Swap used/total; Swap-In/-Out; Page-Faults (major); OOM-Kills; Memory-Pressure (PSI)           | `/proc/meminfo`, `/proc/vmstat`, `/proc/pressure/memory`                                            |
| **Disk-IO**                              | pro Device: read/write Bytes/s, IOPS, await (Latenz), Utilization %, Queue-Länge; IO-Pressure (PSI)                                                         | `/proc/diskstats`, `/proc/pressure/io`                                                              |
| **Dateisysteme**                         | pro Mountpoint: size, used, avail, Inodes used/total, read-only-Flag                                                                                        | `statvfs` + `/proc/self/mountinfo`                                                                  |
| **Netzwerk**                             | pro Interface: rx/tx Bytes/s, Pakete/s, Errors, Drops, Link-Status, Speed; TCP: established, time_wait, Retransmits, Listen-Overflows; conntrack-Auslastung | `/proc/net/dev`, `/sys/class/net`, `/proc/net/snmp`, `/proc/net/netstat`, `/proc/sys/net/netfilter` |
| **System**                               | Uptime, Boot-ID (Reboot-Erkennung), Prozess- und Thread-Anzahl, Zombies, File-Descriptors (genutzt/max), Entropy, Zeit-Synchronisation (NTP synced, Offset, max. Fehler), RAID (degradiert, fehlende Disks, Abgleich) | `/proc/uptime`, `/proc/sys/kernel/random/boot_id`, `/proc/sys/fs/file-nr`, `adjtimex`, `/proc/mdstat` |
| **systemd**                              | failed Units, je Unit eine Serie (`unit`, ≤ 50; erholte 24 h lang als 0, [ADR 0026](adr/0026-zustands-collectors.md))                                                                    | `systemctl list-units --state=failed --plain` (alle 30 s, feste Argumente)                           |
| **Hardware** (opt., automatisch erkannt) | Temperaturen, Lüfter                                                                                                                                        | `/sys/class/hwmon`, `/sys/class/thermal`                                                            |

**Counter** (z. B. Bytes total) rechnet der Agent beim Messen selbst in **Raten pro Sekunde** um, inklusive Reset-
und Wrap-Erkennung, und sendet sie als Gauge. Prozentwerte wie die CPU-Auslastung berechnet er ebenfalls aus Deltas.
Das hat drei Vorteile:

**Ganzzahlige Werte bevorzugt:** Raten und Mengen sendet der Agent als ganze Zahlen in Basiseinheiten (Bytes/s, Pakete/s,
Anzahl), Anteile als ppm (1 000 000 = 100 %). Ganzzahlige Werte komprimieren in TimescaleDB auf ca. 3 Byte/Messwert;
Gleitkommawerte mit voller Genauigkeit brauchen etwa das Dreifache (Spike M0-09). Das Runden auf Nachkommastellen
bringt keine Ersparnis.

- Die Werte bleiben auch nach einer Zwischenspeicherung im WAL-Puffer korrekt.
- Die Continuous Aggregates kommen mit einfachen `avg`/`min`/`max`/`last` aus.
- Der Server braucht keine Window-Funktionen.

Für Summen über Zeiträume (z. B. „übertragene GB heute“) wird `avg × Bucket-Dauer` aufsummiert.

### 8.2 Detailansichten

Jede Detailseite hat oben eine Kopfzeile mit Kennzahlen und darunter synchronisierte Charts (gemeinsamer Zeitraum,
Crosshair-Sync, Zoom per Drag). Jede Kennzahl hat ein ⓘ mit Erklärung.

- **CPU:** gestapelte Modi; Heatmap pro Kern; Load vs. Kernzahl; PSI; Top-Prozesse nach CPU (aus Live-Snapshots, wenn
  aktiv); Steal-Time-Hinweis bei VMs.
- **Memory:** Zusammensetzung (used/cache/buffers/available) gestapelt; Swap; Page-Faults; OOM-Kill-Events als Marker;
  PSI; Top-Prozesse nach RSS.
- **Disk:** pro Device Durchsatz, IOPS, Latenz und Utilization; pro Mountpoint Füllstand und Inodes; **Füllprognose**
  („voll in ~3 Tagen“, lineare Regression über 6 h/24 h).
- **Netzwerk:** pro Interface Durchsatz, Pakete, Fehler und Drops; TCP-Zustände; Retransmits; conntrack.

---

## 9. Live-Ansicht („top“)

- **Wird nur bei Bedarf aktiviert:** Öffnet ein User die Live-Ansicht, schickt der Server `StartLiveView{interval=2s}`
  an den Agent. Mehrere Betrachter teilen sich einen Stream (Fan-out im Server). 30 s nach dem letzten Betrachter folgt
  `StopLiveView`. Die Daten werden **nicht gespeichert**.
- **Im Server** (M3-14): `LiveViewService.WatchProcesses` mit dem Recht `use_live_view` für die Gruppe des Nodes.
    - Der Server erneuert die Lease des Agents alle 20 s, solange jemand zusieht.
    - Ist der Agent nicht verbunden, bekommt der Betrachter `offline` und bleibt verbunden. Lehnt der Agent ab
      (`allow_live_view = false`), endet der Stream mit dem Grund.
    - Rechte, Sichtbarkeit des Nodes und der Org-Schalter werden alle 30 s geprüft; nach 15 min endet der Stream, der
      Client verbindet sich neu. Es gelten die Grenzen der Live-Streams (§18, T26).
    - Nur die Instanz mit der Session des Agents kann liefern ([ADR 0015](adr/0015-topologie-und-betriebsmodell.md),
      Nachtrag M3-14).
- **Inhalt:** Prozessliste mit PID, PPID, User, Status, CPU %, MEM % / RSS, Threads, Laufzeit, Befehlszeile (gekürzt);
  Kopfzeile mit CPU, Mem, Swap, Load und Uptime (wie `top`).
- **Bedienung** (M3-15, Reiter „Prozesse (Live)“ der Node-Seite; der Stream läuft nur, solange er offen ist):
    - Sortieren, Filtern (User, Name, Regex; im Browser, eine ungültige Regex wird angezeigt), Baumansicht (pstree)
      und Pause, bei der die Daten im Hintergrund weiterlaufen.
    - Detail-Drawer pro Prozess: vollständige, redigierte Befehlszeile, Elternprozess, systemd-Unit und Container.
      Offene FDs liest der Agent nicht (siehe Rechte-Hinweis).
    - Alle Texte des Hosts erscheinen nur als Text; Steuer- und Bidi-Zeichen werden markiert (wie im Log-Explorer).
- **Nur lesen.** Es gibt keine Kill- oder Restart-Aktionen (D19).
- **Datenschutz:** Kommandozeilen können Secrets enthalten. Die agentseitige **Redaction** (§10.3) gilt auch hier;
  zusätzlich kann pro Org „cmdline ausblenden“ aktiviert werden (Admin mit Step-up, im Audit-Log). Der Agent sendet die
  Befehlszeilen dann nicht, und der Server entfernt sie zusätzlich selbst.
    - Seit M3-29 pro Betrachter: Befehlszeilen sieht nur, wer die Logs des Nodes lesen darf (Viewer einer Org ohne
      Logs für Viewer nicht). Der Agent sendet sie, solange ein Betrachter sie sehen darf; den anderen entfernt sie
      der Server. Redigiert wird mit den Argumenten: Das Argument nach einer Passwort-Option ist ganz maskiert.
    - Ein Agent liefert höchstens einen Snapshot pro halbem Intervall; schnellere verwirft der Server.
- **Rechte-Hinweis:** Als User `sermon` sind `/proc/<pid>/io` und `fd/` fremder Prozesse nicht lesbar. Diese Felder
  zeigen „n/a“ mit erklärendem ⓘ. Es gibt bewusst **kein** `CAP_SYS_PTRACE` (siehe §24).

---

## 10. Logs

### 10.1 Quellen

| Quelle                 | Umsetzung                                                                                                                                  | Default                                                   |
|------------------------|--------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------|
| **systemd-journal**    | `journalctl -o json --all -f` mit eigenem Cursor (`--after-cursor`, ab systemd 239), feste Feldliste ohne `_CMDLINE`: `MESSAGE`, `PRIORITY`, `SYSLOG_IDENTIFIER`, `SYSLOG_FACILITY`, `_SYSTEMD_UNIT`, `_PID`, `_COMM`, `_BOOT_ID`, `_TRANSPORT`, `_UID`, `_CAP_EFFECTIVE` | an (falls vorhanden)                                      |
| **Klassische Dateien** | `/var/log/sermon/security.log` (SerMon-Vorlage für rsyslog), `/var/log/auth.log`, `/var/log/secure`, `/var/log/messages`: die erste vorhandene ([ADR 0025](adr/0025-logdateien-ohne-journald.md)) | an, wenn kein journald (Alpine)                           |
| **Docker**             | über die Docker-API (`/var/run/docker.sock`), stdout/stderr pro Container mit Labels `container`, `image`, `compose_project`               | **aus** (Modul, siehe §15 – Socket bedeutet root-Rechte!) |
| **Beliebige Dateien**  | Globs pro Node-Gruppe zentral konfiguriert, z. B. `/var/log/nginx/*.log`; Multiline-Regeln (z. B. Java-Stacktraces). Gelesen wird nur unter `log_file_prefixes` der `agent.toml` (Standard `/var/log`), nie State-Verzeichnis, `/etc/sermon`, `/proc`, `/sys`, `/dev`, die Dateien des Journals und die Login-Protokolle (`wtmp`, `btmp`, `lastlog`); Symlinks aufgelöst und bei jedem Öffnen erneut geprüft, nur reguläre Dateien (M3-28); höchstens 64 Dateien, rotierte und komprimierte nicht (M3-05) | aus                                                       |

### 10.2 Verarbeitung

- Der Agent normalisiert jede Zeile zu `{ts, source, unit/file, severity, message, fields}` (`LogEntry`, M3-01):
  - **Unit** ist die systemd-Unit, sonst der Syslog-Identifier.
  - **Severity** kommt aus `PRIORITY` bzw. aus `facility.priority` der busybox-Zeilen. Die übrigen Dateiformate
    nennen keine, dann bleibt sie unbekannt.
  - **Felder:** Identifier, PID, Prozessname, UID des Absenders, Boot-ID, Transport, Facility.
  - **Form:** Werte mit falscher Form (Boot-ID, Transport, Facility) lässt der Agent weg, damit ein Eintrag nie den
    ganzen Batch ungültig macht.
  - **Batch:** höchstens 1 000 Einträge bzw. 256 KiB.
- Loki-Labels werden **bewusst sparsam** gewählt (niedrige Cardinality): `node_id`, `source`, `unit`, `severity`. Alles
  andere steht als *structured metadata* bzw. in der Zeile.
- **Begrenzungen:**
  - Zeilenlänge max. 16 KB (danach gekürzt + Flag).
  - Im Agent höchstens 500 Einträge und 512 KiB pro Sekunde (mit Spitzen bis 5 000 bzw. 5 MiB), Rest verworfen und
    gezählt (M3-04).
  - Logs belegen höchstens die Hälfte des Puffers. Darüber werden sie verworfen, damit Metriken und Security-Events
    ihren Platz behalten.
  - Der Server begrenzt zusätzlich: 1 MiB/s pro Node, 3 MiB/s pro Org, 7 Tage alt bis 5 min voraus, höchstens 64 Units
    pro Node (danach `other`) (M3-07). Gezählt wird jeder Eintrag mit seinen Metadaten (M3-28).
  - Lehnt Loki die Einträge einer Org für deren Limits ab (429), werden nach 30 s nur die Einträge dieses Nodes
    verworfen; das ist kein Ausfall von Loki (M3-28).
- **Ausfall von Loki:** Bestätigt wird erst nach dem Push. Schlägt er fehl, wartet die Bestätigung höchstens 30 s,
  dann werden die Logs verworfen und gezählt, und Loki gilt 30 s als ausgefallen. So halten Logs Metriken und Alerts
  nie lange auf; das Self-Monitoring meldet den Ausfall ([ADR 0031](adr/0031-logs-in-loki.md), Nachtrag M3-07).
- **Versand:** Der Journal- bzw. Dateiverbraucher gibt jeden Eintrag an die Security-Parser und an den Log-Versand.
  Dieser wählt nach der Konfiguration der Gruppe aus (ADR 0030), redigiert und bündelt (bis 1 s oder 256 KiB). Der
  Cursor bzw. die Position rückt erst vor, wenn Events und Logs im WAL liegen. Vor der ersten Konfiguration und mit
  `allow_logs = false` gehen keine Logs raus.

### 10.3 Redaction (im Agent, vor dem Versand)

- **Standardregeln** (M3-02, `sermon-security::redact`):
  - Schlüssel-Wert-Paare wie `password=`, `passwd`, `secret`, `token`, `api_key` (auch mit Präfix wie `DB_PASSWORD`,
    in JSON und als `--password wert`)
  - `Authorization: Bearer|Basic …` und Passwörter in URLs bzw. Connection-Strings
  - private Schlüsselblöcke, auch gekürzt; eine Zeile nur aus Base64 in der Länge einer Schlüsselzeile (journald
    schreibt einen Schlüssel Zeile für Zeile)
  - Tokens, die man ohne Schlüssel davor erkennt: AWS-Access-Keys, JWTs, GitHub- und Slack-Tokens,
    Enrollment-Tokens (`sme_…`)
- **Regeln pro Org:** Regex mit Ersatz `***`. Hat die Regex eine Gruppe `secret`, wird nur diese ersetzt, sonst der
  ganze Treffer; leere Treffer ändern nichts. Höchstens 32 Regeln mit je 1 KiB Muster und 1 MiB kompilierter Größe.
  Regeln werden **kompiliert und getestet**, bevor der Server sie verteilt; ReDoS-sicher durch die Rust-`regex`-Crate
  (lineare Laufzeit).
- **Reihenfolge und Grenzen:** erst die Standardregeln, dann die der Org. Ein `***` kann länger sein als der Treffer;
  die Nachricht wird danach erneut auf 16 KiB gekürzt. Dieselbe Redaction gilt für Logs, `sudo.command` und
  Kommandozeilen der Live-Ansicht.

### 10.4 Log-UI

- Explorer mit Zeitbereich, Node/Gruppe, Quelle/Unit, Severity und Volltextsuche. Die LogQL-Abfrage wird
  **serverseitig** aus strukturierten Filtern gebaut; User schreiben kein LogQL.
- Live-Tail (Server-Streaming), Kontext-Ansicht („± 50 Zeilen um diesen Eintrag“), Histogramm der Log-Rate nach
  Severity.
- Darstellung **immer als Text** (kein HTML), ANSI-Codes werden entfernt oder sicher gerendert. Lange Zeilen werden
  umbrochen.
- Absprung aus Charts und Alerts: „Logs dieses Nodes um diesen Zeitpunkt“.

---

## 11. Security-Events

Security-relevante Ereignisse werden **im Agent** aus Journal- bzw. Auth-Log geparst und als **strukturierte Events**
übertragen. Das funktioniert auch, wenn der Log-Versand ansonsten abgeschaltet ist. Gespeichert werden sie in PostgreSQL
(abfragbar und für 1 Jahr aufbewahrt).

Übertragung und Speicherung (M2-07):

- **Agent:** Er sammelt Events bis zu 500 oder 1 s und schreibt sie als Batch ins WAL. Erst danach rückt der
  Journal-Cursor vor.
- **Server:**
  - Er speichert die Batches in derselben Reihenfolge wie die Metrik-Batches und dedupliziert über die
    Batch-Sequenznummer (`nodes.event_seq`, in derselben Transaktion gesetzt).
  - Pro Node gelten zwei Token-Buckets, die über Reconnects erhalten bleiben: 50 Events/s bei 5 000 Burst für die
    Arten, die jeder von außen auslösen kann (fehlgeschlagene und ungültige Logins, Abbrüche vor der Anmeldung), und
    5 Events/s bei 500 Burst für die übrigen. Eine Flut von Fehlversuchen verdrängt so nicht den erfolgreichen Login
    danach (M2-37); Verworfenes zählt pro Art (`/metrics`), verworfene seltene Events meldet das Self-Monitoring.
  - Event-Zeiten sind höchstens 7 Tage alt und 5 min in der Zukunft.
- **Tabelle:** `security_events` mit RLS, nach Monat partitioniert und für die Anwendung nur anhängbar. Typisierte
  Spalten für Art, User, IP (`inet`), Host und Port, die übrigen Felder als `details`.
- **Abfrage:** `SecurityEventService.ListSecurityEvents` mit Zeitraum, Nodes, Arten, User und IP/CIDR. Sie gilt für
  Gruppen mit `view_security_events` (Viewer nur, wenn die Org Logs für Viewer erlaubt).
- **Bekannte Quellen** (M3-16): Der Ingest merkt sich pro Node, welcher User sich von welcher Quelle erfolgreich
  angemeldet hat (`known_login_sources` mit RLS). Quelle ist die Adresse, bei IPv6 das /64, sonst der Hostname aus dem
  sshd-Log; Anmeldungen ohne beides (Konsole) zählen nicht. Die erste Anmeldung eines Users von einer Quelle trägt
  `new_source`.
    - Eine Quelle, die ein Jahr ungenutzt ist, wird vergessen und gilt danach wieder als neu.
    - Pro Node gibt es höchstens 10 000 Quellen. Darüber wird eine unbekannte Quelle weder gemerkt noch als neu markiert;
      gefälschte Zeilen aus Logdateien ([ADR 0025](adr/0025-logdateien-ohne-journald.md)) füllen die Tabelle so nicht.
    - Seit wann SerMon die Quellen eines Nodes lernt, steht in `nodes.learning_since`: die Anmeldung des Nodes, für
      ältere Nodes das Update auf M3-16. Die Events vor dem Update kann die Migration wegen RLS nicht lesen.
- **Auswertungen** (M3-16): `GetSecurityOverview` liefert für einen Zeitraum die Fehlversuche
  (`ssh.login.failed`, `ssh.login.invalid_user`) pro Node in höchstens 200 Intervallen, die zehn Adressen und User
  mit den meisten Fehlversuchen (mit Zahl der Nodes, letztem Zeitpunkt und den unverifizierten) und Summen:
  Logins, Logins von neuen Quellen, Root-Logins und sudo-Fehler. `ListSecurityEvents` filtert auf neue Quellen.
- **CSV-Export** (M3-16): `ExportSecurityEvents` mit denselben Filtern, höchstens 10 000 Events und 8 MiB. Zellen, die
  mit `=`, `+`, `-`, `@`, Tab oder CR beginnen, bekommen ein führendes Apostroph (CSV-Injection).

| Event                                 | Erkennung (Beispiele)                                                                   | Felder                                      |
|---------------------------------------|-----------------------------------------------------------------------------------------|---------------------------------------------|
| `ssh.login.failed`                    | `Failed password for (invalid user )?<u> from <ip> port <p> ssh2`, `Failed publickey …`, `error: PAM: Authentication failure for <u> from <ip>` (keyboard-interactive, ohne Port) | ts, user, ip, port, method, invalid_user    |
| `ssh.login.invalid_user`              | `Invalid user <u> from <ip> port <p>`                                                   | ts, user, ip, port                          |
| `ssh.login.success`                   | `Accepted (password\|publickey\|keyboard-interactive/pam) for <u> from <ip> port <p>`   | ts, user, ip, port, method, key_fingerprint |
| `ssh.preauth.disconnect`              | `Connection closed by authenticating user … [preauth]`                                  | ts, user, ip, port                          |
| `sudo.auth.failed`                    | `pam_unix(sudo:auth): authentication failure … user=<u>`                                | ts, user, tty                               |
| `sudo.not_allowed`                    | `<u> : user NOT in sudoers ; … USER=<t> ; COMMAND=…` (auch „command not allowed“)       | ts, user, target, tty                       |
| `sudo.command`                        | `sudo: <u> : … COMMAND=<cmd>` (optional, standardmäßig aus)                             | ts, user, target, cmd (redacted)            |
| `account.created` / `account.deleted` | `useradd`/`userdel`-Meldungen                                                           | ts, user, uid (created)                     |
| `system.reboot`                       | Wechsel der Boot-ID                                                                     | ts, previous_boot, uptime_before            |
| `system.oom_kill`                     | Kernel `Out of memory: Killed process <pid> (<name>)`                                   | ts, pid, process                            |
| `enrollment.token_used`               | vom Server: Enrollment oder Übernahme mit einem Token ([ADR 0027](adr/0027-server-ereignisse-und-self-monitoring.md))                  | ts, token, ip                               |
| `node.locked`                         | vom Server: vorsorgliche Sperre (zweite Session, ersetztes Zertifikat)                  | ts, reason                                  |
| `node.rotation_new_ip`                | vom Server: Schlüsselrotation von einer neuen Adresse                                   | ts, ip, previous_ip                         |

**Parser-Anforderungen:** Sie müssen sowohl `sshd` als auch `sshd-session` erkennen (Prozessname ab OpenSSH 9.8), IPv4
und IPv6, und Distro-Varianten. **Jeder Parser wird mit echten Logzeilen aus allen Zieldistros als Fixtures getestet und
gefuzzt** (`cargo-fuzz`).

**Vertrauen** ([ADR 0023](adr/0023-vertrauen-in-security-events.md)):

- **Nur privilegierte Absender:** Events entstehen nur aus Zeilen mit `_UID` 0 oder Capabilities (`_CAP_EFFECTIVE`,
  setzt journald). Ein `logger -t sshd …` eines normalen Users erzeugt nichts.
- **OOM-Kills** zählen nur über den Kernel-Transport.
- **Adresse und Port vom Zeilenende:** Den User-Namen wählt der Angreifer, deshalb kommen Adresse und Port vom Ende
  der Zeile.
- **Escapes:** sshd schreibt Nicht-ASCII in User-Namen als Oktal-Escape (`j\303\274rgen`). Der Agent decodiert das.
- **Reboot** heißt: Die Boot-ID wechselt zwischen zwei Zeilen. Nach einem Neustart des Agents zählt die Boot-ID aus dem
  gespeicherten Cursor. `uptime_before` ist die monotone Zeit der letzten Zeile vor dem Wechsel. Ohne journald
  vergleicht der Agent beim Start die Boot-ID des Kernels mit der gespeicherten (verifiziert, ohne `uptime_before`).
- **Keine Quelle:** Findet der Agent weder journald noch eine lesbare Logdatei, meldet er das in den Host-Fakten; die
  Node-Seite sagt, dass Security-Events fehlen, und nennt `install.sh --grant-log-access` bzw. die rsyslog-Vorlage.
- **Ohne journald** ([ADR 0025](adr/0025-logdateien-ohne-journald.md)): Zeilen aus Logdateien zählen, sind aber
  „nicht verifiziert“ – jeder lokale User kann sie schreiben. Verifiziert sind sie nur mit der SerMon-Vorlage für rsyslog
  und root als Absender; sudo bleibt dort unverifiziert (setuid, reale UID des Users). Ein Alert mit einem solchen Event
  trägt `source: unverified`; UI und Kanäle sagen es.

**Anreicherung im Server (optional, M3-18):** das Land einer Adresse aus einer **lokalen** Datenbank im MaxMind-DB-Format,
die der Betreiber bereitstellt (`SERMON_GEOIP_DB_FILE`, z. B. DB-IP Lite unter CC BY 4.0 oder GeoLite2 Country). Der
Server lädt nichts herunter (D27) und fragt keinen Dienst. Private, Loopback-, Link-local- und CGNAT-Adressen haben kein
Land. Das Dashboard zeigt den Lizenzhinweis der Daten, der Export eine Spalte `country`; ASN gibt es nicht. Für „Login
von neuer IP“ dienen die bekannten Quellen pro Node und User (M3-16, oben).

---

## 12. Alerting

### 12.1 Konzepte

- **Regel (Rule):** Definition mit Typ, Bedingung, Schwellen, Dauer und Labels. Sie gilt für alle Nodes oder für einen
  Selektor (Gruppe/Tags).
- **Alert-Instanz:** Regel × Node (× ggf. Dimension wie Mountpoint/Interface) mit Zustand.
- **Zustandsautomat:** `OK → PENDING → WARNING | CRITICAL → RESOLVED → OK`, dazu `SILENCED` und `ACKNOWLEDGED` als
  Flags.
    - `for:` legt die Mindestdauer bis zum Feuern fest (beim Verlassen von OK; ein feuernder Alert eskaliert sofort zu
      CRITICAL), `clear:` eine eigene Rückfall-Schwelle (**Hysterese**: zwischen Schwelle und `clear` bleibt er auf
      WARNING).
    - **Quittieren** stoppt die Benachrichtigungen, bis der Alert gelöst ist oder zu CRITICAL eskaliert. Operatoren der
      Gruppe quittieren; Security-Alerts brauchen einen frischen Step-up.
    - **Flapping-Erkennung:** Mehr als N Wechsel in M Minuten setzen den Status `FLAPPING`; Benachrichtigungen werden
      dann gedrosselt.
- **Notification-Policy (Routing):** Matcher auf Severity, Gruppe, Regel (Muster wie `security.*`), die Labels der
  Regel und die Tags des Nodes → Kanäle. Dazu Wiederholungsintervall, Gruppierung (pro Node oder Digest je Auswertungsrunde) und eine
  einfache **Eskalation** („nach 15 min ohne Quittierung zusätzlich an Kanal X“). Ohne eine Policy gehen alle Alerts
  an alle aktiven Kanäle der Org. Während eine Instanz flattert, werden Feuern und Behoben nicht gemeldet
  ([ADR 0019](adr/0019-alerting.md), Nachtrag M2-14).
- **Silences und Wartungsfenster:** zeitlich begrenzt, per Selektor (Matcher wie bei den Policies, dazu einzelne
  Nodes). Sie schalten Benachrichtigungen stumm, nicht die Auswertung; was nach dem Ende noch feuert, kommt einmal.
  Wöchentliche Wartungsfenster pro Gruppe schalten Reboot- und Offline-Alerts stumm; während Update-Wartungsfenstern
  geschieht das **automatisch** ([ADR 0019](adr/0019-alerting.md), Nachtrag M2-15).
- **Auswertung:** Die Engine läuft im Server alle 10 s. Schwellwert-Regeln fragen den `MetricStore` über die typisierte
  Query-Schicht ab (Fenster bis 15 min aus dem Ringpuffer, längere aus TimescaleDB), Event-Regeln lesen die neuen Security-Events aus PostgreSQL (`received_at`). Ein Strom im Prozess ginge nicht: Ingest und Auswertung einer Org laufen auf verschiedenen Server-Instanzen (Lease, M2-07).

### 12.2 Regeltypen (YAML-Format)

User schreiben **keine freien Queries** (Security und Einfachheit). Stattdessen gibt es typisierte Regelarten:

```yaml
apiVersion: sermon/v1
kind: AlertRule
metadata:
  id: system.cpu.usage_high
  name: { de: "Hohe CPU-Auslastung", en: "High CPU usage" }
  labels: { category: cpu }
spec:
  type: threshold            # threshold | rate | event | absence | state | anomaly | prediction
  metric: sermon_cpu_utilization_ratio
  labels: { mode: idle }
  complement: true           # Auslastung = 1 − Leerlauf: Schwellen und Wert des Alerts sind die Auslastung
  aggregate: avg             # avg | max | min | last
  window: 10m
  warning: { op: ">", value: 0.85 }
  critical: { op: ">", value: 0.95 }
  clear: { op: "<", value: 0.80 }
  for: 5m
  selector: { groups: [ "*" ] }
  help:
    de: "Die CPU war über 10 Minuten im Mittel stark ausgelastet. Prüfe in der Live-Ansicht, welche Prozesse die Last erzeugen."
    en: "…"
---
apiVersion: sermon/v1
kind: AlertRule
metadata: { id: security.ssh.bruteforce, name: { de: "SSH-Brute-Force", en: "SSH brute force" } }
spec:
  type: event
  event: ssh.login.failed
  groupBy: [ ip ]
  count: { window: 5m, warning: 10, critical: 50 }
  notifyFields: [ ts, user, ip, port ]
```

Die Regeltypen im Einzelnen (M2-03):

- **`threshold`:** Aggregat über das Fenster (`avg`, `max`, `min`, `last`). Kein `sum`, weil eine Summe über
  Zeit-Buckets von deren Größe abhängt.
- **`rate`:** Rate pro Sekunde oder Minute (`per`), optional relativ zu einer zweiten Rate mit denselben Labels
  (Fehler pro Paket).
- **`absence`:** Sekunden seit dem letzten Kontakt des Nodes.
- **`state`:** letzter Wert, optional nur, wenn vorher eine Bedingung galt (`seen`: Interface war up).
- **`prediction`:** Stunden, bis eine fallende Metrik null erreicht (lineare Tendenz).
- **`event`** (M2-08): zählt Security-Events einer Art (`event`) pro Node und `groupBy` (`ip`, `user`) und
  vergleicht mit `count.warning`/`count.critical` (mindestens so viele im Fenster `count.window`).
  - Gezählt wird nach der Entstehung der Events, gelesen werden die im Fenster angekommenen. Ein Rückstau nach einem
    Ausfall des Agents löst so beim Eintreffen aus, wenn er einen Burst enthält.
  - IPv6-Adressen zählen pro /64.
  - `match` filtert auf Feldwerte (`user: root`), `after` verlangt vorausgehende Events derselben Quelle auf dem Node
    („Erfolg nach 5 Fehlversuchen in 1 h“), `count.of: nodes` zählt die Nodes der Org mit derselben Quelle
    (Fleet-Korrelation; jeder beteiligte Node bekommt einen Alert).
  - `notifyFields` nennt die Felder des neuesten Events für die Benachrichtigung (ohne Steuerzeichen, höchstens
    256 Bytes je Wert).
  - `learning` (M3-16) lässt die Events eines Nodes so lange nach `nodes.learning_since` aus (höchstens 30 Tage):
    Auf einem neuen Node ist jede Quelle neu.
  - Ein Alert hält, solange die Zahl reicht; das Fenster ersetzt `clear`. Pro Regel und Runde gibt es höchstens 100
    Instanzen und 50 000 gelesene Events.

Gemeinsam gilt:

- Filter und Dimensionen einer Regel nennen zusammen alle Labels der Metrik. So gehört zu jeder Instanz genau eine
  Reihe.
- Ausschlüsse sind Glob-Muster auf Dimensionen (`/dev/*`).
- Verhältnisse werden als 0..1 verglichen.

Format und Ablage (M2-04):

- **Schema:** Das Schema ist die Proto-Nachricht `sermon.web.v1.AlertRule` mit protovalidate. YAML wird als JSON dieser
  Nachricht gelesen. Fehler sind unbekannte Felder (mit Zeile und Pfad), Felder eines anderen Regeltyps und
  Verhältnisse außerhalb von 0..1 („85“ statt „0.85“).
- **Weitere Felder:**
  - `dimensions` (eine Instanz pro Labelwert) und `exclude` (Glob-Listen je Dimension);
  - `relativeTo` (geteilt durch eine zweite Metrik mit denselben Labels, z. B. Inodes belegt/gesamt);
  - `complement` (`threshold` auf einem Verhältnis: verglichen wird `1 − Wert`, Punkt für Punkt vor dem Aggregat, so
    ist `max` der Auslastung `1 −` das Minimum des Leerlaufs; nicht mit `relativeTo` oder `increase`). Damit zeigt der
    CPU-Alert bei 97 % Last 97 % statt der 3 % Leerlauf, aus denen er rechnet;
  - `per` (`second` | `minute`), `seen`/`seenWindow` und `lookback`;
  - `flapping` (`changes`, `window`; Standard 5 Wechsel in 30 min);
  - `selector.groups` (`*` oder Gruppen-IDs).
  - Dauern werden als `90s`, `10m` oder `24h` angegeben.
- **Standardregeln:** Sie liegen in `rules/<kategorie>/<id>.yaml`. Das Verzeichnis ist das Label `category`, der
  Dateiname die ID. Der Server bettet sie ein.
- **Overrides pro Org:** Eine Org kann eine Standardregel abschalten, durch ein eigenes Regeldokument ersetzen oder
  eigene Regeln ergänzen; das Original bleibt unverändert. Lädt ein gespeichertes Override nicht mehr (z. B. nach einer
  Katalogänderung), wird es protokolliert und ignoriert. Es gilt dann die Standardregel.
- **Ende von Alerts:** Gilt eine Regel für einen Node nicht mehr (abgeschaltet, entfernt, Node außerhalb ihrer
  Gruppen), endet ihr Alert: Ein feuernder wird `RESOLVED`, die Instanz entfällt.

Import und Export laufen über die UI bzw. die API. Der Import wird **validiert** (dieselbe Prüfung wie oben) und zeigt
einen **Dry-Run-Diff**.

### 12.3 Standardregeln (angelehnt an die Netdata-Health-Configs)

| Kategorie         | Regel                                                          | Typ               | Warnung / Kritisch (Default)         |
|-------------------|----------------------------------------------------------------|-------------------|--------------------------------------|
| **Verfügbarkeit** | Node offline (kein Heartbeat)                                  | absence           | – / 60 s                             |
|                   | Reboot erkannt                                                 | event             | ✓ (Info)                            |
|                   | Agent-Puffer läuft voll / Daten verworfen                      | threshold         | 50 % / 90 %                          |
|                   | Uhrzeit nicht synchronisiert / Offset                          | state / threshold | > 100 ms / > 1 s                     |
| **CPU**           | CPU-Auslastung (avg 10 min)                                    | threshold         | 85 % / 95 %                          |
|                   | iowait (avg 10 min)                                            | threshold         | 20 % / 40 %                          |
|                   | Steal-Time (VMs)                                               | threshold         | 10 % / 25 %                          |
|                   | Load15 pro Kern                                                | threshold         | > 1,5 / > 3                          |
| **Memory**        | RAM-Nutzung (avail)                                            | threshold         | 80 % / 90 %                          |
|                   | Swap-Nutzung                                                   | threshold         | 50 % / 80 %                          |
|                   | OOM-Kill                                                       | event             | – / jeder                            |
|                   | Memory-Pressure (PSI some avg60)                               | threshold         | 10 % / 30 %                          |
| **Disk**          | Dateisystem-Füllstand                                          | threshold         | 80 % / 90 %                          |
|                   | Inodes                                                         | threshold         | 80 % / 90 %                          |
|                   | Füllprognose: voll in < 24 h / < 6 h                           | prediction        | ✓ / ✓                              |
|                   | Disk-Utilization (avg 10 min)                                  | threshold         | 90 % / 98 %                          |
|                   | Dateisystem read-only geworden                                 | state             | – / ✓                               |
|                   | RAID degradiert (`/proc/mdstat`)                               | state             | – / ✓                               |
| **Netzwerk**      | Interface down (das vorher up war)                             | state             | ✓                                   |
|                   | Empfangs-/Sende-Errors, Drops                                  | rate              | > 0,1 % / > 1 %                      |
|                   | Bandbreiten-Sättigung (bei bekannter Speed)                    | threshold         | 80 % / 95 %                          |
|                   | TCP-Listen-Queue-Overflows                                     | rate              | > 0 / > 10/min                       |
|                   | conntrack-Tabelle                                              | threshold         | 80 % / 90 %                          |
| **System**        | File-Descriptors                                               | threshold         | 80 % / 90 %                          |
|                   | Zombie-Prozesse                                                | threshold         | > 10 / > 50                          |
|                   | systemd-Unit failed (pro Unit)                                 | state             | ✓                                   |
|                   | Uhr nicht synchronisiert (30 min)                              | state             | ✓ / –                               |
|                   | Temperatur (falls vorhanden)                                   | threshold         | hwmon-Grenzen                        |
| **Security**      | SSH-Brute-Force pro IP (5 min)                                 | event             | 10 / 50                              |
|                   | Dieselbe IP auf ≥ 3 Nodes (Fleet-Korrelation)                  | event             | ✓                                   |
|                   | **Erfolgreicher Login nach vielen Fehlversuchen** derselben IP | event             | – / ✓                               |
|                   | Root-Login per SSH erfolgreich                                 | event             | ✓                                   |
|                   | Login von neuer Quelle (M3-16) / neuem Land                    | event             | ✓ (Warnung)                         |
|                   | sudo-Authentifizierungsfehler                                  | event             | 3 / 10 in 10 min                     |
|                   | sudo ohne Berechtigung („not in sudoers“)                      | event             | ✓                                   |
|                   | Neuer Benutzer angelegt                                        | event             | ✓                                   |
| **Updates**       | Security-Updates verfügbar                                     | state             | > 0 seit 3 Tagen / > 0 seit 14 Tagen |
|                   | Updates verfügbar (alle)                                       | state             | Info                                 |
|                   | Reboot erforderlich                                            | state             | > 1 Tag / > 7 Tage                   |
|                   | Update-Lauf fehlgeschlagen                                     | event             | – / ✓                               |
| **Anomalien**     | Anomalie-Rate des Nodes hoch                                   | anomaly           | 10 % / 25 % der Metriken             |

Seit M2-04 mitgeliefert sind die Regeln für Verfügbarkeit, CPU, Memory, Disk, Netzwerk und System, soweit ihre Daten
vorliegen, seit M2-08 die Event-Regeln. Abweichungen und offene Punkte:

- **Agent-Puffer:** Die Schwellen sind absolut, 100 MiB und 180 MiB. Der Puffer fasst fest 200 MiB, der Agent meldet
  sein Limit aber nicht.
- **„Daten verworfen“** (M2-24): `threshold` mit der Aggregation `increase` (Zuwachs des kumulativen Zählers in 1 h,
  Warnung ab 1). Dazu die Regeln für Ereignisse des Servers: Token benutzt (Warnung), Node gesperrt (kritisch),
  Rotation von neuer Adresse (Warnung) – [ADR 0027](adr/0027-server-ereignisse-und-self-monitoring.md).
- **Netzwerk:** Errors und Drops gelten getrennt für Empfang und Senden, jeweils relativ zu den Paketen.
- **Zurückgestellt:** Bandbreiten-Sättigung (Durchsatz in Bytes/s, Link-Speed in Bits/s; dem Regelformat fehlt ein
  Umrechnungsfaktor) und Temperatur (hwmon-Grenzen meldet der Agent nicht).
- **Event-Regeln (M2-08):** Reboot (15 min), OOM-Kill (15 min), SSH-Brute-Force (5 min), Fleet-Korrelation
  (15 min), Erfolg nach mindestens 5 Fehlversuchen derselben Adresse in 1 h, Root-Login, sudo-Fehler, sudo ohne
  Berechtigung (neu) und neuer Benutzer. „Info“ gibt es als Stufe nicht; Reboot meldet eine Warnung.
- **Zustände (M2-22):** RAID degradiert (kritisch), systemd-Unit failed (Warnung, eine Instanz pro Unit), Uhr nicht
  synchronisiert (Warnung nach 30 min) – [ADR 0026](adr/0026-zustands-collectors.md).
- **Login von neuer Quelle** (M3-16): `security.ssh.login_new_source` meldet eine Warnung pro User und Quelle, wenn
  SerMon den Node schon 7 Tage kennt (`learning: 168h`); „Info“ gibt es als Stufe nicht.
- **Später:** „Login aus neuem Land“ braucht GeoIP (§11, M3-18). Updates und Anomalien kommen in späteren
  Meilensteinen.

### 12.4 Benachrichtigungskanäle

| Kanal                                       | Umsetzung                                                                                 |
|---------------------------------------------|-------------------------------------------------------------------------------------------|
| E-Mail                                      | SMTP mit STARTTLS/TLS-Pflicht (konfigurierbar), HTML- und Text-Template                   |
| Webhook                                     | JSON-POST, Header `X-SerMon-Signature: t=<ts>,v1=<HMAC-SHA256>`, Retries mit Backoff      |
| Slack, Microsoft Teams, Discord, Mattermost | Incoming Webhooks / Workflows; formatierte Nachrichten (Blocks / Adaptive Cards / Embeds) |
| Telegram                                    | Bot-API (Token + Chat-ID), HTML mit escapten Werten                                       |
| ntfy, Gotify, Pushover                      | jeweilige HTTP-APIs, Priorität aus Severity                                               |

- Alle Kanäle implementieren ein gemeinsames Interface `Notifier` (Adapter-Pattern) und haben einen **„Test
  senden“-Button**.
- Templates sind **i18n-fähig** (Sprache pro Kanal) und enthalten einen Deep-Link in die UI.
- Zugangsdaten werden **verschlüsselt gespeichert** (§18.5). Ausgehende Requests sind **SSRF-geschützt** (§18.6).
- Zustellungen werden protokolliert (Status, Fehlertext) und sind in der UI einsehbar.

Umsetzung des Frameworks (M2-10):

- **Kanal:** Typ, Name, Sprache (de/en), an/aus, Einstellungen und Zugangsdaten. Jeder Typ beschreibt seine Felder
  (Text, URL, Auswahl, Zahl; geheim oder nicht, Pflicht), daraus baut die UI das Formular.
  - Zugangsdaten werden mit `secrets.Box` versiegelt (Kontext `notification_channel:<org>:<id>`) und sind nur
    schreibbar: Antworten nennen nur, welche gesetzt sind.
  - URLs prüft schon das Speichern gegen die Outbound-Policy von `safehttp`.
  - Anlegen, Ändern und Löschen brauchen `manage_channels` und einen frischen Step-up. Ein gekapertes Konto soll Alerts
    mit Security-Events nicht umleiten können. Jede Änderung steht im Audit-Log, ohne Ziel und Zugangsdaten.
- **Nachricht:** `Render` erzeugt Titel, Zeilen und Deep-Link (`/nodes/<id>`) in der Sprache des Kanals. Werte
  erscheinen in ihrer Einheit, etwa Verhältnisse als Prozent mit dem Dezimalzeichen der Sprache. Werte der Hosts
  bleiben auf ihrer Zeile.
- **Escaping:** Jeder Kanal escapt für sein Format. Helfer gibt es für HTML, Markdown (jede ASCII-Interpunktion),
  Slack (`& < >`) und gegen `@everyone`/`@channel`.
- **Zustellung:** `notification_deliveries` ist Warteschlange und Protokoll zugleich.
  - Jede Server-Instanz holt fällige Zeilen mit `FOR UPDATE SKIP LOCKED` und einem Claim von 2 min.
  - Höchstens 6 Versuche: nach 30 s, 1, 2, 4 und 8 min. HTTP 429 wartet wie verlangt, höchstens 1 h.
  - 4xx, gesperrte Ziele und gelöschte oder abgeschaltete Kanäle scheitern sofort.
  - Ein Event kann doppelt ankommen, geht aber nicht verloren.
- **Fehlertexte** enthalten nie die Zugangsdaten des Kanals, stehen auf einer Zeile und sind höchstens 500 Bytes lang.
- **„Test senden“:** Die Nachricht geht sofort raus, ohne Retry, steht im Protokoll und ist auf 5 pro Minute und Org
  begrenzt.

Kanäle der ersten Welle (M2-11):

- **E-Mail:** über den SMTP-Zugang des Betreibers (nur wenn `SERMON_SMTP_*` gesetzt ist).
  - Text- und HTML-Teil; das HTML rendert `html/template`, das jeden Wert für seinen Kontext escapt.
  - Eine Mail pro Empfänger, höchstens 10 Empfänger.
  - Absagen des Mailservers mit 5xx sind endgültig.
- **Webhook:** JSON-POST mit `X-SerMon-Event` und `X-SerMon-Signature: t=<Unix-Sekunden>,v1=<HMAC-SHA256>`. Die
  Signatur geht über `<t>.<Body>`; das Geheimnis hat mindestens 16 Zeichen.
  - Der Body enthält Art, Severity, Regel, Node, Dimension (als Labels), Wert mit Einheit und Text, Felder,
    Titel, Text und Link (Version 1; neue Felder kommen nur dazu).
  - Die Prüfung beim Empfänger beschreibt die Hilfe (`channels/webhook`).
- **ntfy:** JSON-Publish an die Wurzel des Servers mit Titel, Text, Link, Tag und Priorität (kritisch 5, Warnung 4,
  behoben und flatternd 3, quittiert und stabil 2).
  - Das Topic ist ein Geheimnis: Auf einem öffentlichen Server liest mit, wer es kennt.
  - Ein Access-Token ist optional.
- Die HTTP-Kanäle teilen sich einen `safehttp`-Client. Ein Fehler zitiert höchstens 200 Bytes der Antwort, auf einer
  Zeile und ohne die Zugangsdaten des Kanals.

Kanäle der zweiten Welle (M2-12): Die Webhook-URL enthält jeweils das Zugriffstoken und wird als Geheimnis
gespeichert.

- **Slack:** Block Kit mit farbiger Leiste, Kopfzeile, Werten und Button. Werte der Hosts stehen nur in
  `plain_text`-Objekten, die Slack wörtlich zeigt; der mrkdwn-Fallback ist escapt. Nur URLs unter
  `https://hooks.slack.com/`.
- **Microsoft Teams:** Adaptive Card über einen Workflow („Beim Empfang einer Teams-Webhookanforderung posten“).
  TextBlock und FactSet rendern Markdown, darum stehen Werte nur in `TextRun`-Elementen. Jede https-URL ist erlaubt,
  weil Workflow-URLs je Tenant und Region wechseln.
- **Discord:** Embed mit Farbe, Feldern und Link, `allowed_mentions` leer. Werte werden Markdown-escapt und ihre
  Erwähnungen entschärft, gekürzt auf die Grenzen von Discord. Nur offizielle Webhook-URLs.
- **Mattermost:** Slack-kompatibles Attachment. Werte werden Markdown-escapt, `@all`, `@channel` und `@here` darin
  entschärft. Jede URL ist erlaubt (selbst betrieben; intern nur mit der Allowlist).

Kanäle der dritten Welle (M2-13):

- **Telegram:** `sendMessage` der Bot-API im HTML-Modus, Titel fett. Werte werden HTML-escapt und ihre Erwähnungen
  entschärft; Link-Vorschau aus. Der Link zum Node steht als Text am Ende, weil Buttons Links auf `localhost`
  ablehnen. Gekürzt auf 4096 Zeichen; quittierte und wieder stabile Alerts kommen ohne Ton. Das Bot-Token ist ein
  Geheimnis und steht im Pfad: Fehler nennen nur den Host. Die Wartezeit nach 429 steht im Body
  (`parameters.retry_after`). Nur `https://api.telegram.org`.
- **Gotify:** `POST /message` mit dem App-Token im Header `X-Gotify-Key`, als reiner Text (`text/plain`), Priorität
  8 (kritisch), 5 (Warnung), 3 (behoben), 2 (quittiert, stabil). Selbst betrieben, auch unter einem Pfad.
- **Pushover:** Formular an `https://api.pushover.net/1/messages.json` als reiner Text, Priorität 1 (kritisch),
  0 oder −1 (quittiert, stabil); keine Notfall-Priorität. Titel und Nachricht auf 250 bzw. 1024 Zeichen gekürzt.
  App-Token und User-Key sind Geheimnisse, Geräte optional.

---

## 13. Anomalie-Erkennung

**Ziel:** Ungewöhnliches Verhalten erkennen, ohne Schwellen festzulegen. Die Erkennung soll **erklärbar** sein („CPU ist
5× höher als dienstags um diese Uhrzeit üblich“).

### Verfahren (serverseitig, Worker jede Minute auf den 1-min-Aggregaten)

1. **Kandidaten-Metriken:** ein kuratierter Satz von ca. 30 Kernmetriken pro Node (CPU-Modi, Load, Mem available,
   Swap-IO, Disk-IO/Latenz, Net rx/tx, TCP-Retransmits, Prozessanzahl, Log-Rate pro Severity, SSH-Fehlversuche/h).
2. **Baselines pro (Node, Metrik):**
    - *kurzfristig:* EWMA von Mittelwert und Varianz (Halbwertszeit ca. 1 h)
    - *saisonal:* Median und **MAD** (Median Absolute Deviation) je **Stunde der Woche** (168 Buckets) über die letzten
      4 Wochen
3. **Score:** robuster Z-Score `z = |x − median| / (1,4826 · MAD)`. Die Metrik ist anomal, wenn **beide** Verfahren
   übereinstimmen (z > 4 bzw. EWMA-Abweichung > 4σ). Das reduziert Fehlalarme.
4. **Glättung:** anomal erst bei ≥ 3 von 5 Minuten (debounce).
5. **Anomalie-Rate pro Node:** Anteil der anomalen Metriken in Prozent (wie die *Anomaly Rate* bei Netdata). Sie dient
   als Alert-Quelle (§12.3).
6. **Lernphase:** Neue Nodes und Metriken brauchen **7 Tage** Lernzeit. Bis dahin zeigt die UI „lernt noch (Tag 3/7)“
   und es gibt keine Anomalie-Alerts.

### UI

- **Anomalie-Band** über jedem Chart (rote Markierung in anomalen Zeitabschnitten) und optional das **Baseline-Band**
  (erwarteter Bereich) im Chart.
- **„Was war ungewöhnlich?“:** Für einen markierten Zeitbereich werden alle Metriken eines Nodes (oder der Fleet) nach
  Score sortiert. Das hilft bei der Ursachensuche (vergleichbar mit Netdatas *Metric Correlations*).

### Qualitätssicherung

- Synthetische Testreihen (Rauschen + Saisonalität + injizierte Spikes, Level-Shifts, Drifts) als Golden-Tests.
- Ein Benchmark für Precision/Recall läuft in CI und darf nicht schlechter werden.
- Ausbaustufe (v2): Isolation Forest bzw. k-means auf Feature-Vektoren als zusätzlicher Detektor hinter demselben
  Interface `Detector`.

---

## 14. Paket-Updates und Auto-Update

### 14.1 Erfassung (Agent → über den Helper)

Alle 6 h, nach Paketmanager-Läufen (erkannt über Dateiänderungen an dpkg-/rpm-Datenbanken) und auf Anforderung:

| Distro        | Verfügbare Updates                         | Security-Updates                         | Reboot nötig                            |
|---------------|--------------------------------------------|------------------------------------------|-----------------------------------------|
| Debian/Ubuntu | `apt-get update` + `apt list --upgradable` | Origin/Suite `*-security`                | `/var/run/reboot-required(.pkgs)`       |
| RHEL-Familie  | `dnf check-update`                         | `dnf updateinfo list --security`         | `dnf needs-restarting -r`               |
| Alpine        | `apk update` + `apk version -l '<'`        | ⚠️ keine Security-Metadaten → nur „alle“ | laufender Kernel ≠ installierter Kernel |

Gemeldet werden: Anzahl gesamt und Security, Paketliste (Name, installiert, Kandidat, Quelle, Security-Flag), Reboot
nötig samt Grund, Zeitpunkt des letzten erfolgreichen Updates.

### 14.2 Update-Policies (pro Node-Gruppe)

```yaml
kind: UpdatePolicy
spec:
  mode: security-auto        # report | security-auto | all-auto
  window: { days: [ sun ], start: "03:00", duration: 2h, timezone: "Europe/Berlin" }
  autoReboot: true           # nur innerhalb des Fensters und nur wenn nötig
  rollout: { maxParallel: 1, stopOnFailure: true, healthCheck: 10m }
```

- **Rolling Rollout:** Pro Gruppe laufen höchstens N Nodes gleichzeitig. Nach Update und Reboot muss der Node innerhalb
  von `healthCheck` wieder online und ohne kritische Alerts sein, sonst stoppt der Rollout und ein Alert wird ausgelöst.
- **Manuell:** Ein Operator kann „Jetzt aktualisieren“ auslösen (Step-up-2FA, Bestätigungsdialog mit Paketliste).
- **Ausführung (Helper):**
  `DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade`
  (bzw. nur Security-Pakete); `dnf -y upgrade [--security]`; `apk upgrade`. Ausgabe und Exit-Code gehen an den Server,
  gekürzt und redacted.
- **Transparenz:** Updates-Dashboard (§16), Historie pro Node, vollständiges Audit-Log (wer, wann, was, Ergebnis).
- **Lokales Veto:** `allow_remote_updates=false` im Helper (§4.2).

### 14.3 Agent-Self-Update

- Bei Installation per Paket-Repo aktualisiert sich der Agent über den normalen Paketmanager (und damit über die
  Policy).
- Bei Installation per Skript lädt der Helper die neue Binary, **prüft die Signatur gegen den eingebetteten Public
  Key**, ersetzt sie atomar und startet sie neu. Ein Rollback erfolgt, wenn der Agent nach 2 min nicht verbunden ist.
- **Kanäle:** `stable` / `beta` pro Gruppe; Ringe für einen gestaffelten Rollout.

---

## 15. Integrationen und Module

- Module werden **in den Agent kompiliert** (Cargo-Features, alle im Release-Build enthalten), sind **standardmäßig
  deaktiviert** und werden **zentral** pro Node-Gruppe aktiviert und konfiguriert (UI-Formular aus einem JSON-Schema).
- **Auto-Discovery als Vorschlag:** Der Agent erkennt laufende Dienste (Port, Prozessname, Unit) und die UI zeigt „nginx
  erkannt – Modul aktivieren?“. Er aktiviert **nichts** automatisch.
- Einheitliches Trait `Module { fn schema(); fn validate(cfg); async fn collect(ctx) -> Metrics/Events }`. Panics werden
  isoliert, jedes Modul hat ein eigenes Zeit- und Ressourcenbudget.
- Zugangsdaten für Module (z. B. DB-Passwort) werden Ende-zu-Ende verschlüsselt an den Agent übertragen und dort nur im
  Speicher gehalten. **Empfehlung in der UI:** einen read-only-Monitoring-User verwenden.

| Modul (v1)                    | Inhalt                                                                 | Hinweis                                                                                                                         |
|-------------------------------|------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------|
| **Docker**                    | Container-Status, CPU/Mem/Net/IO pro Container, Restarts, Health; Logs | ⚠️ Zugriff auf `docker.sock` entspricht root-Rechten → deutliche Warnung; alternativ cgroup-basiert (ohne Socket, nur Metriken) |
| **systemd-Units (erweitert)** | Zustand und Restarts ausgewählter Units                                | ohne Zusatzrechte                                                                                                               |
| **nginx**                     | `stub_status`                                                          |                                                                                                                                 |
| **Apache httpd**              | `server-status?auto`                                                   |                                                                                                                                 |
| **PostgreSQL**                | Verbindungen, TPS, Cache-Hit, Replikations-Lag, DB-Größen, Locks       | Rolle `pg_monitor`                                                                                                              |
| **MySQL/MariaDB**             | Verbindungen, Queries, Slow Queries, Replikation, InnoDB-Buffer        |                                                                                                                                 |
| **Redis**                     | Speicher, Clients, Ops/s, Hit-Rate, Evictions                          |                                                                                                                                 |
| **TLS-Zertifikate**           | Ablaufdatum lokaler Dateien und Endpunkte                              | Alert 21 / 7 Tage                                                                                                               |
| **HTTP-/TCP-Check**           | Erreichbarkeit, Latenz und Statuscode lokaler Endpunkte                |                                                                                                                                 |
| **SMART**                     | Gesundheit und Temperatur von Disks                                    | benötigt `smartctl` über den Helper (neue feste Op)                                                                             |
| **Prometheus-Scraper**        | beliebige `/metrics`-Endpunkte (nur localhost bzw. Allowlist)          | deckt den „Rest der Welt“ ab; Serien-Limit                                                                                      |

Spätere Kandidaten: Fail2ban, WireGuard, ZFS, Proxmox, Kubernetes (Node-Level), RabbitMQ, Elasticsearch.

---

## 16. Dashboards

### Fest eingebaute Dashboards (v1)

1. **Fleet-Übersicht (Startseite)**
    - Kachel-Raster aller sichtbaren Nodes: Ampel (Alerts), CPU/Mem/Disk als Mini-Gauges, Sparkline der letzten 15 min,
      Uptime, Badges für Updates und Reboot
    - Kopfzeile: Nodes online/offline, aktive Alerts nach Severity, Nodes mit Security-Updates, Anomalie-Rate der Fleet
    - Umschaltbar zwischen Kachel- und Tabellenansicht; gruppiert nach Gruppe/Tag; Filter und Suche
2. **Node-Übersicht:** Kopfzeile (Host-Fakten, Uptime, Agent-Version), Kerncharts, aktive Alerts, letzte Events
   (Reboots, Logins, Updates), Tabs: CPU · Memory · Disk · Netzwerk · Prozesse (Live) · Logs · Security · Updates ·
   Module.
3. **Alerts:** aktive Alerts (gruppiert, filterbar, quittierbar), Timeline/Historie, Statistik („lauteste Regeln“,
   MTTA/MTTR).
    - Statistik (M3-19): Reiter „Statistik“ für 7 oder 30 Tage aus `alert_history`. Eine Auslösung beginnt beim
      Wechsel von einem ruhigen Zustand auf Warnung oder kritisch; MTTA zählt bis zur ersten Quittung, MTTR bis zur
      Behebung, Auslösungen vor dem Zeitraum fehlen. Die zehn lautesten Regeln mit Nodes, offenen und flatternden
      Auslösungen; höchstens 200 000 Übergänge pro Auswertung.
    - Jeder Alert führt in die Charts seines Nodes eine Stunde vor bis nach dem Auslösen (Zeitraum und Reiter in der
      URL der Node-Seite) und in die Logs um den Zeitpunkt.
4. **Security-Dashboard:**
    - Fehlgeschlagene Logins über Zeit (gestapelt nach Node), **Top-IPs, Top-Usernamen**, Länder (bei GeoIP), Weltkarte
      optional
    - Erfolgreiche Logins mit „neu“-Markierung, Root-Logins, sudo-Fehler
    - Tabelle: Zeitstempel · Node · User · IP · Port · Methode · Ergebnis, mit Export als CSV
    - Hinweise wie „Passwort-Login ist auf 4 Nodes erlaubt“ (später: SSH-Config-Audit)
    - Umgesetzt mit M3-17 unter `/security` (Menüpunkt „Security“; die eigene Kontosicherheit steht seitdem im
      Kontomenü) und als Reiter „Security“ der Node-Seite. Der Filter steht in der URL; Summen, Top-Listen und Chart
      filtern die Tabelle mit einem Klick. Jede Zeile führt in die Logs ±15 min; „neu“ und „unverifiziert“ sind mit
      Symbol und Text markiert. Mit GeoIP (M3-18) stehen die Länder bei den Top-IPs und in der Tabelle; eine Weltkarte gibt es nicht.
5. **Updates/Patch-Compliance:** Nodes nach Status (aktuell / Updates / Security-Updates / Reboot nötig), Alter offener
   Security-Updates, nächste Wartungsfenster, Laufhistorie.
6. **Kapazitätsplanung:** Füllprognosen aller Dateisysteme (sortiert nach „Tage bis voll“), RAM/CPU-Trends über 30 Tage,
   Top-N-Verbraucher der Fleet.
7. **Fleet-Heatmap:** Nodes × Zeit, eingefärbt nach CPU, Mem, IO oder Anomalie-Rate. Damit fallen Muster und „Noisy
   Neighbors“ sofort auf.
8. **Vergleich:** 2–4 Nodes nebeneinander bzw. übereinandergelegt (z. B. vor und nach einer Änderung, oder
   Cluster-Mitglieder).
9. **Anomalien:** Nodes nach Anomalie-Rate, „Was war ungewöhnlich?“ für einen Zeitraum.

### Ausbau (v1.x)

- **Eigene Dashboards:** Grid-Layout mit Drag & Drop, Widgets (Zeitreihe, Gauge, Stat, Tabelle, Log-Stream,
  Alert-Liste), Variablen (Node/Gruppe), Teilen innerhalb der Org, Export/Import als JSON.
- **Kiosk-/TV-Modus:** Vollbild, Auto-Rotation, ohne Bedienelemente; Zugriff über einen **schreibgeschützten,
  widerrufbaren Kiosk-Token**, der an Dashboard und Org gebunden ist.
- **SLA-/Uptime-Report:** monatlich als PDF/HTML pro Gruppe.

---

## 17. UI/UX und Hilfe

- **Layout:** Linke Navigation (Übersicht · Nodes · Alerts · Logs · Security · Updates · Dashboards · Einstellungen),
  Org-Switcher, globale Suche bzw. **Command-Palette (Ctrl/⌘+K)** für Nodes, Seiten und Aktionen.
- **Zeit:** Globaler Zeitbereich-Picker (Presets + absolut), Live-Modus mit Auto-Refresh; alle Charts einer Seite sind
  **synchronisiert** (Crosshair, Zoom).
- **Design:** Light/Dark/System, klare Typografie, Statusfarben **nicht nur über Farbe** (zusätzlich Icon oder Text),
  responsive bis Tablet; das Smartphone wird für Alerts und die Übersicht optimiert.
- **Barrierefreiheit:** Ziel **WCAG 2.2 AA**, Tastaturbedienung, Screenreader-Labels, automatische axe-Checks in CI.
- **Hilfe zum Inhalt:**
    - **ⓘ-Tooltips** an jeder Metrik und Kennzahl („Was ist iowait?“, „Was bedeutet Steal-Time?“) mit kurzem Text und
      Link „Mehr erfahren“
    - **Kontexthilfe-Panel** (rechts ausklappbar, `?`-Taste) pro Seite: Wozu dient die Seite, wie lese ich die Charts,
      typische Ursachen und nächste Schritte
    - **Alert-Hilfe:** Jede Regel hat einen Hilfetext (`help`), der in UI und Benachrichtigung erscheint: Was bedeutet
      der Alert, welche Ursachen sind typisch, was ist zu tun?
    - **Single Source of Truth:** Hilfetexte als Markdown unter `docs/help/<locale>/…`. Daraus werden UI-Tooltips,
      Panels und die Online-Doku gebaut, und ein Lint prüft auf fehlende Übersetzungen.
    - **Empty States mit Anleitung** (z. B. „Noch keine Nodes – so installierst du den Agent“), Onboarding-Assistent für
      den ersten Node, Tour optional
- **i18n:** DE und EN von Anfang an (Paraglide). Zahlen, Einheiten und Datumsformate nach Locale; Einheiten automatisch
  skaliert (KiB/MiB/GiB, bit/s vs. Byte/s umschaltbar).
- **Performance:** Erstes Laden < 200 KB JS (gzip), mit 10 KiB Luft für Flotte und Node-Seite (`budget.e2e.ts`); was
  erst beim Öffnen gebraucht wird (Popover, Formulare, Dialoge), lädt bei Bedarf. Charts mit Downsampling im Client
  (uPlot), Virtualisierung für lange Listen (Logs, Prozesse).
- **Keine externen Ressourcen (D27):** Alle Bibliotheken, Schriften (über `@fontsource`-Pakete), Icons und Bilder kommen
  per npm ins Projekt. Sie werden beim Build gebündelt und vom SerMon-Server selbst ausgeliefert. Es gibt keine CDNs,
  keine Google Fonts und kein Analytics. Abgesichert wird das durch eine CSP ohne fremde Origins und einen E2E-Test,
  der jeden Request an einen fremden Host als Fehler wertet.

---

## 18. Security-Konzept

> Ziel: **OWASP ASVS Level 2** für Server und UI, **Least Privilege** auf den Nodes, nachvollziehbare **Supply Chain**.

### 18.1 Threat Model (STRIDE, Auszug)

| Bedrohung              | Szenario                                        | Gegenmaßnahmen                                                                                                                                                                                |
|------------------------|-------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Spoofing               | Fremder Agent gibt sich als Node aus            | mTLS mit Client-Zertifikat, Identität nur aus dem Zertifikat, Denylist, kurze Laufzeiten                                                                                                      |
| Spoofing               | Gestohlener Agent-Schlüssel wird parallel genutzt bzw. rotiert | Nur eine Session pro Node, Alert bei zweiter Session oder gescheiterter Rotation, Rotation auf Abruf mit sofortiger Sperre (§6) |
| Spoofing               | Gestohlener Enrollment-Token                    | 1 h TTL, einmalig, gruppengebunden, nur Hash in der DB, Audit + Benachrichtigung bei Nutzung                                                                                                  |
| Tampering              | MITM zwischen Agent und Server                  | TLS 1.3, CA-Pinning bzw. Fingerprint, keine Downgrade-Optionen                                                                                                                                |
| Tampering              | Manipulierte Agent-Binary                       | Signaturprüfung (minisign/cosign) im Install-Skript und beim Self-Update, signierte Paket-Repos                                                                                               |
| Repudiation            | „Wer hat das Update ausgelöst?“                 | Append-only-Audit-Log (Hash-Kette), inkl. Helper-Ops                                                                                                                                          |
| Information Disclosure | User sieht Nodes einer fremden Org/Gruppe       | Scope-Pflicht in jeder Repository-Methode, PG-RLS, Scope-Filter im `MetricStore`, `X-Scope-OrgID` bei Loki, **Tenant-Isolation-Tests** in CI                                                                              |
| Information Disclosure | Secrets in Logs/cmdlines                        | Redaction im Agent, verschlüsselte Secrets, keine Secrets in Server-Logs                                                                                                                      |
| DoS                    | Ein Agent flutet den Server                     | Quotas, Cardinality-Limits, Batch-Größenlimits, Throttle                                                                                                                                      |
| DoS                    | Login-Brute-Force                               | Rate-Limits, progressive Delays, 2FA                                                                                                                                                          |
| Elevation of Privilege | Kompromittierter Agent → root                   | Agent ohne root, systemd-Hardening, Helper mit festem Op-Enum + Peer-Credential-Check                                                                                                         |
| Elevation of Privilege | Kompromittierter Server → alle Nodes            | **Keine freien Kommandos**; der Helper führt nur Paketmanager-Updates aus Distro-signierten Quellen aus; lokales Veto; Self-Update nur mit Signatur des **Release-Keys** (nicht des Servers!) |
| XSS / Injection        | Präparierte Logzeile, Hostname oder Prozessname | Ausgabe nur als Text (Svelte escaped standardmäßig, **kein `{@html}`**), strikte CSP, parametrisierte SQL-Queries (sqlc), LogQL nur aus typisierten Bausteinen mit Escaping         |

### 18.2 Transport

- **Extern:** HTTPS über Caddy (automatisches ACME oder eigenes Zertifikat), TLS 1.2+ (bevorzugt 1.3), HSTS (setzt der Server selbst, sobald alle erlaubten Origins `https://` sind).
- **Betriebsmodus:** `SERMON_MODE=production` (Standard) erlaubt nur `https://`-Origins und verlangt SMTP mit
  STARTTLS oder TLS (geprüftes Zertifikat). `development` lockert beides für `just dev` (Mailpit, `http://`) und
  wird beim Start als Warnung geloggt ([ADR 0022](adr/0022-betriebsmodus-und-einladungen-per-mail.md)).
- **Agent-Kanal:** eigener Port oder eigener Hostname mit **mTLS**, **TLS 1.3 only**, Client-Zertifikat Pflicht. Die
  interne Agent-CA ist getrennt vom Web-Zertifikat. Der CA-Schlüssel ist verschlüsselt gespeichert; signiert wird mit
  einer Intermediate-CA. Rotation von Agent-Zertifikaten, Server-Zertifikat und CA (Trust-Bundle) regelt §6
  „Schlüsselrotation“.

### 18.3 Web-Application-Security

- **Security-Header:**
  `Content-Security-Policy: default-src 'self'; script-src 'self' 'sha256-…'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `Permissions-Policy` restriktiv, COOP/CORP.
  Die Hashes der Inline-Skripte und der festen `style`-Attribute aus dem eigenen Bundle berechnet der Server beim
  Start aus dem eingebetteten Build (`'unsafe-hashes'` nur für genau diese Werte; ADR 0003).
- **Keine Drittanbieter-Origins (D27):** Die CSP erlaubt nur `'self'`; die UI lädt nichts von externen Servern.
  Externe Links (z. B. zur Doku) sind nur normale Links mit `rel="noopener noreferrer"`.
- **CSRF:** SameSite-Cookies, Connect-Requests mit `Content-Type: application/proto|json` (kein „simple request“) und
  **Origin-Prüfung** im Server.
- **Autorisierung:** zentral (`authz`), standardmäßig verweigernd (deny by default), getestet über Rollen-Matrix-Tests
  (jede RPC × jede Rolle).
- **Eingaben:** Validierung an der Grenze (Protovalidate-Regeln im Schema), Größenlimits für alle Requests.
- **Ausgaben:** keine Stacktraces an Clients, generische Fehlermeldungen mit Korrelations-ID.

### 18.4 Agent-Hardening (systemd)

```ini
[Service]
User=sermon
Group=sermon
SupplementaryGroups=systemd-journal adm
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
ReadWritePaths=/var/lib/sermon-agent
ReadOnlyPaths=/etc/sermon
MemoryMax=128M
CPUQuota=20%
```

Ziel: `systemd-analyze security sermon-agent` mit einem Score **< 2,0** („OK“). Die ausgelieferte Unit
(`packaging/systemd/sermon-agent.service`, ADR 0012) ist zusätzlich gehärtet und erreicht 1,9 (seit M2-22 ohne `ProtectClock`, damit der Agent `adjtimex` lesen darf, [ADR 0026](adr/0026-zustands-collectors.md)); `ProtectHome=read-only`
statt `yes`, damit der Dateisystem-Collector auch `/home` sieht. Unter OpenRC wird der Agent über
`supervise-daemon` mit `command_user=sermon` gestartet.

### 18.5 Secrets

- Zugangsdaten für Kanäle und Module, der TOTP-Seed und der CA-Schlüssel werden mit **AES-256-GCM** verschlüsselt. Der
  Master-Key kommt aus `SERMON_MASTER_KEYS_FILE` (Docker-Secret); Schlüsselrotation ist mit Key-ID im Ciphertext möglich.
- Passwörter werden nie gespeichert oder geloggt; Tokens sind nur als Hash abgelegt.
- Secrets werden in der UI nach dem Speichern **nie wieder angezeigt** (nur „gesetzt ✓ / ändern“).

### 18.6 Ausgehende Verbindungen (SSRF)

- Webhooks und Chat-Kanäle gehen über den Client `safehttp` ([ADR 0024](adr/0024-ausgehende-verbindungen-safehttp.md)):
  - Private, Loopback-, Link-local-, Metadata- und andere Sonderadressen (`169.254.169.254` usw.) sind **blockiert**,
    auch in IPv6-Formen mit eingebetteter IPv4 (IPv4-mapped, NAT64, 6to4).
  - Die IP wird aufgelöst und **gepinnt** (gegen DNS-Rebinding); der Socket prüft sie noch einmal.
  - Höchstens 3 Redirects, jeder neu geprüft und ohne Wechsel auf http; 15 s Zeitlimit, Antwort höchstens 1 MiB.
  - Öffentliche Ziele nur über https; http nur zu Zielen auf der Allowlist. Proxy-Variablen werden ignoriert.
  - Fehler nennen nur den Host, nie Pfad oder Query (Webhook-URLs enthalten Geheimnisse).
  - Die **Allowlist** für interne Ziele (z. B. internes Mattermost) setzt der Betreiber in
    `SERMON_OUTBOUND_ALLOWLIST` (CIDRs). So kann auch ein übernommenes Superadmin-Konto das interne Netz nicht
    öffnen. Metadata-Dienste lassen sich nicht freigeben.
- Keine Telemetrie und keine „Phone-Home“-Funktion. Die Prüfung auf neue Versionen ist abschaltbar.
- Das gilt auch für die Fremdkomponenten: **TimescaleDB** (`TIMESCALEDB_TELEMETRY=off`) und **Loki**
  (`analytics.reporting_enabled: false`) senden standardmäßig Nutzungsdaten. Beides wird in jeder Compose-Datei
  abgeschaltet und durch einen Test geprüft (Entwicklungsumgebung: M0-07).

### 18.7 Datenschutz (DSGVO)

- IP-Adressen, Usernamen und Logs sind **personenbezogene Daten**. Daraus folgen: eine definierte Retention,
  Rollenbeschränkung (Logs für Viewer abschaltbar), Löschfunktion, Export und eine Vorlage für das
  Verarbeitungsverzeichnis in `docs/`.
- GeoIP nur lokal. Optional werden IPs nach X Tagen pseudonymisiert (gekürzt).

### 18.8 Prozess

- **Security-Review-Checkliste** pro PR (siehe Agent-Skill `sermon-security-review`), SAST in CI (CodeQL, gosec, clippy,
  semgrep), Dependency-Scans, Fuzzing der Parser.
- Vor v1.0 ein **externer Pentest** bzw. ein Review durch eine zweite Person.
- `SECURITY.md` mit Responsible-Disclosure-Prozess und GitHub Private Vulnerability Reporting.

---

## 19. Qualität: Clean Code, TDD und Prozess

### 19.1 Prinzipien

- **TDD (Red → Green → Refactor)** als Standard für Domänenlogik, Parser, Alert-Engine, Anomalie-Erkennung und Authz.
  Für Adapter wird *test-first* auf Integrationsebene (Testcontainers) gearbeitet.
- **Clean Architecture / Hexagonal:** Domäne ohne I/O; Abhängigkeiten zeigen nach innen; Ports und Adapter; Dependency
  Injection per Konstruktor (Go: ohne Framework; Rust: Generics/Traits).
- **SOLID, kleine Funktionen, sprechende Namen**, keine Magic Numbers (Konstanten bzw. Konfiguration), Fehler explizit
  (Go: `errors.Is/As` mit Wrapping; Rust: `thiserror` in Libraries, `anyhow` nur in Binaries).
- **Make illegal states unrepresentable:** starke Typen (`NodeID`, `OrgID`, `Percent`, `Bytes`) statt `string`/
  `float64`.
- **Boy-Scout-Rule**, aber Refactorings in eigenen Commits.
- **Dokumentation als Code:** ADRs (`docs/adr/NNNN-titel.md`, MADR-Format), Architekturdiagramme als Mermaid bzw. C4 im
  Repo.

### 19.2 Teststrategie

| Ebene                        | Rust (Agent)                                                                                             | Go (Server)                                                                                                     | Web                                      |
|------------------------------|----------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|------------------------------------------|
| Unit                         | `cargo test`; `/proc`-Fixtures pro Distro unter `agent/fixtures/`; `proptest`; `insta`-Snapshots         | Table-driven Tests, `testing/quick` bzw. `rapid`                                                                | Vitest + Testing Library                 |
| Fuzzing                      | `cargo-fuzz` für alle Parser (proc, Logzeilen, Security-Events)                                          | `go test -fuzz` für Regel-Parser und Query-Builder                                                              | –                                        |
| Integration                  | Agent gegen einen Mock-gRPC-Server                                                                       | **Testcontainers-go** mit PG/TimescaleDB und Loki                                                             | –                                        |
| Contract                     | `buf breaking` gegen `main`                                                                              | dto.                                                                                                            | generierte Clients                       |
| E2E                          | –                                                                                                        | **docker-compose-Testumgebung:** Server + Agents in Containern (Debian, Ubuntu, Rocky, Alma, Fedora, Alpine; arm64 nightly auf nativem Runner, [ADR 0006](adr/0006-ci-und-supply-chain.md)) | **Playwright** (inkl. axe-Accessibility) |
| Security                     | Rollen-Matrix-Tests, Tenant-Isolation-Tests (User A darf keine Daten aus Org B sehen – für **jede** RPC) |                                                                                                                 |                                          |
| Performance                  | Benchmarks (`criterion`) für Collectors; Ressourcen-Budget in der E2E-Demo (`scripts/agent-budget.sh`, 60 s, mit `journalctl`) | Ingest-Lasttest (k6 / eigenes Tool: 50 simulierte Agents)                                                       | Lighthouse-Budget                        |
| Mutation (optional, nightly) | `cargo-mutants`                                                                                          | `gremlins`                                                                                                      | Stryker                                  |

**Coverage-Ziele:** Domäne und Parser ≥ 90 %, gesamt ≥ 80 %. Coverage ist ein Indikator, kein Ziel an sich:
Mutationstests zeigen die tatsächliche Testgüte.

### 19.3 Tooling und Konventionen

- **Format/Lint:** `rustfmt`, `clippy -D warnings` (dazu ausgewählte `pedantic`-Lints); `gofumpt`, `golangci-lint` (u.
  a. `gosec`, `errcheck`, `revive`, `staticcheck`); `prettier`, `eslint`, `svelte-check`; `buf lint`; `shellcheck`;
  `hadolint`; `markdownlint`.
- **Task-Runner:** `just` (`just dev`, `just test`, `just lint`, `just e2e`, `just gen`).
- **Git-Hooks:** `lefthook` (Format, Lint, schnelle Tests, Secret-Scan mit `betterleaks`).
- **Commits:** Conventional Commits → automatischer Changelog und SemVer (`release-please`).
- **Branching:** Trunk-based, kurze PRs; `main` ist geschützt (Reviews, grüne CI, signierte Commits empfohlen).
- **Dev-Umgebung:** `deploy/dev/compose.yaml` (PG/TimescaleDB, Loki, Mailpit für SMTP-Tests, ntfy) und ein Devcontainer; ein
  lokaler Agent-Simulator (`sermon-agent simulate`, `just simulate`) erzeugt Fake-Nodes, mit Szenarien
  (`--scenario cpu-high|disk-filling|ssh-bruteforce|flapping|offline`) für Tests der Alerts von Hand.

### 19.4 Definition of Done

- [ ] Tests geschrieben (zuerst), grün, Coverage nicht gesunken
- [ ] Lint, Format und Typecheck grün; keine neuen `unsafe`/`unwrap()` ohne Begründung
- [ ] Security-Checkliste durchlaufen (Authz, Tenant-Scope, Input-Validierung, Secrets, Logging)
- [ ] i18n-Texte DE und EN vorhanden, Hilfetexte für neue Metriken und Regeln
- [ ] Doku bzw. ADR aktualisiert, Changelog-Eintrag (über den Commit)
- [ ] Bei Protokolländerungen: `buf breaking` grün oder bewusst versioniert

---

## 20. Repository-Struktur

```
SerMon/
├── agent/                         # Rust-Workspace
│   ├── Cargo.toml
│   ├── crates/
│   │   ├── sermon-agent/          # Binary: Orchestrierung, CLI (enroll, run, uninstall, simulate)
│   │   ├── sermon-helper/         # Binary: root-Helper (minimal)
│   │   ├── sermon-core/           # Domänentypen, Traits (Collector, LogSource, EventParser, Clock)
│   │   ├── sermon-collectors/     # /proc-, /sys-Collectors
│   │   ├── sermon-logs/           # journald, Dateien, Docker
│   │   ├── sermon-security/       # Security-Event-Parser
│   │   ├── sermon-buffer/         # WAL
│   │   ├── sermon-transport/      # gRPC-Client, Enrollment, Zertifikatsrotation
│   │   ├── sermon-updates/        # Parser für apt/dnf/apk (im Agent, Ausgaben vom Helper)
│   │   └── sermon-modules/        # Integrationen (Feature-Flags)
│   ├── fixtures/                  # echte /proc-Snapshots + Logzeilen je Distro
│   └── fuzz/
├── server/                        # Go-Modul
│   ├── cmd/sermon-server/
│   ├── internal/
│   │   ├── domain/                # Entitäten, Value Objects, Regeln (rein)
│   │   ├── app/                   # Use-Cases, authz
│   │   ├── alerting/              # Engine, Zustandsautomat, Regeltypen
│   │   ├── anomaly/               # Detektoren, Baselines
│   │   ├── updates/               # Policy-/Rollout-Orchestrierung
│   │   ├── adapters/
│   │   │   ├── postgres/          # sqlc-Queries, Repositories, RLS
│   │   │   ├── timescale/         # MetricStore: COPY-Ingest, Ringpuffer, typisierte Query-Schicht
│   │   │   ├── loki/
│   │   │   ├── notify/            # email, webhook, slack, teams, discord, mattermost, telegram, ntfy, gotify, pushover
│   │   │   ├── oidc/
│   │   │   └── pki/               # interne Agent-CA
│   │   └── transport/
│   │       ├── agentapi/          # gRPC/mTLS
│   │       └── webapi/            # Connect, Sessions, Middleware (CSP, CSRF, Rate-Limit)
│   ├── migrations/
│   └── web/                       # embed der gebauten SPA
├── web/                           # SvelteKit-SPA
│   ├── src/lib/{components,charts,api,i18n,stores}
│   ├── src/routes/
│   └── tests/{unit,e2e}
├── proto/                         # buf-Module (agent/v1, web/v1)
├── rules/                         # mitgelieferte Alert-Regeln (YAML)
├── packaging/                     # install.sh, nfpm (deb/rpm), apk, systemd-/OpenRC-Units
├── deploy/
│   ├── compose/                   # Produktions-Compose + Caddyfile
│   └── dev/
├── docs/
│   ├── adr/
│   ├── help/{de,en}/              # Hilfetexte (Single Source of Truth)
│   ├── security/                  # Threat Model, Hardening-Guide
│   └── user/ admin/
├── scripts/                       # Hilfsskripte (Git-Hooks, CI)
├── .claude/skills/                # Agent-Skills (siehe §25)
├── .github/workflows/
├── justfile
├── lefthook.yml
├── KONZEPT.md
├── SECURITY.md
└── LICENSE                        # AGPL-3.0
```

---

## 21. CI/CD, Release und Supply Chain

### Pipelines (GitHub Actions)

| Workflow       | Trigger                   | Inhalt                                                                                                                                                                                                                                                                       |
|----------------|---------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `ci.yml`       | PR, push                  | Lint, Format, Unit- und Integrationstests (Matrix: Rust, Go, Web), `buf lint/breaking`, Coverage-Report                                                                                                                                                                      |
| `security.yml` | PR, nightly               | CodeQL (Go, JS/TS, Rust), `gosec`, `govulncheck`, `cargo-audit`, `cargo-deny` (Lizenzen und Advisories), `pnpm audit`, `betterleaks`, `trivy` (Images), OpenSSF Scorecard                                                                                                       |
| `e2e.yml`      | PR (Label) und nightly    | Compose-Umgebung mit Agents auf allen Zieldistros (amd64; arm64 über QEMU nightly), Playwright                                                                                                                                                                               |
| `fuzz.yml`     | nightly, PR (Agent, kurz) | `cargo-fuzz` / `go test -fuzz` mit Zeitbudget, Funde als Artefakt                                                                                                                                                                                                            |
| `release.yml`  | Tag `v*` (release-please) | Cross-Builds (musl amd64/arm64), Server-Image (multi-arch, distroless/static, non-root), `.deb`/`.rpm`/`.apk` (nfpm), **SBOM** (CycloneDX via syft), **cosign keyless signing** + **SLSA-Provenance** (L3), GitHub Release, Paket-Repos aktualisieren (signiert mit GPG/RSA) |

- **Actions per Commit-SHA gepinnt**, minimale `permissions:`, OIDC statt langlebiger Secrets.
- **Renovate** für Abhängigkeiten (gruppiert, Auto-Merge nur für Patch-Releases von Dev-Dependencies).
- **npm:** nur Releases, die mindestens 3 Tage alt sind (`minimumReleaseAge`), exakte Versionen, committetes Lockfile,
  Install-Skripte nur per Allowlist (ADR 0003).
- Reproduzierbare Builds anstreben (`SOURCE_DATE_EPOCH`, `--locked`, `-trimpath`).

### Versionierung

- SemVer für Server und Agent (gemeinsame Version). Der Server unterstützt Agent-Versionen `N` und `N-1` (Minor).
- Datenbankmigrationen nur vorwärts, mit Upgrade-Tests (Fixture-DB der Vorversion → Migration → Tests).

---

## 22. Deployment und Betrieb

### docker-compose (Produktion)

```
services:
  caddy            # TLS (ACME), Reverse-Proxy für Web und Enrollment (80, 443)
  server           # aus dem Quellcode gebaut (distroless, nonroot); Agent-Port 8443 direkt veröffentlicht (mTLS im Server)
  postgres         # PostgreSQL 17 + TimescaleDB (Image timescale/timescaledb, pg17), Volume, nur internes Netz
  loki             # Logs: auth_enabled: true, Compactor-Retention 14 Tage, kein Port, nur internes Netz (ADR 0031)
networks: edge (caddy, server; Ports und ausgehend), internal (server, postgres, loki; internal: true)
```

- **Dateien:** `deploy/compose/` (Anleitung `docs/admin/installation.md`, [ADR 0028](adr/0028-produktions-compose.md)). Jeder Container ist
  `read_only`, ohne Capabilities und ohne neue Privilegien (`just lint-compose` prüft das); Secrets liegen als Dateien
  in `secrets/`, Einstellungen in `.env`. Der Agent-Port geht nicht über Caddy, damit der Server die echten Adressen der
  Agents sieht. Die E2E-Demo läuft gegen genau diese Datei (Overlay `deploy/e2e/compose.yaml`).

- **Erstinstallation:**
  - `sermon-server init --master-keys-file PFAD` legt die Master-Key-Datei an (nie überschrieben), migriert die DB,
    erzeugt die Agent-CA und gibt ein Setup-Token aus. In `deploy/compose` läuft es im einmaligen Service `migrate`, der
    auch bei jedem Start vor dem Server migriert (`sermon-server migrate`): Nur er kennt die Owner-Rolle
    `sermon_owner`, der Server nur `sermon_app`, der Superuser nur der Datenbank-Container (M2-36).
  - Solange die Einrichtung offen ist, schreibt der Server bei jedem Start ein neues Token ins Log; der Weg für
    Docker (`docker compose logs`).
  - Das Token ist 30 min gültig, wird nur als Hash gespeichert und funktioniert genau einmal. Mit ihm legt der
    Setup-Assistent den ersten Superadmin samt erster Org an.
  - Danach verweigert `init` jede weitere Ausführung.
- **Backup:**
  - `sermon-server backup` sichert Stammdaten, Events, CA und Konfiguration verschlüsselt per `pg_dump`, ohne Metrik-Rohdaten.
  - Eine vollständige Sicherung inklusive Metriken läuft physisch per **pgBackRest**.
  - Für Loki gibt es eine Anleitung zur Volume-Sicherung.
  - Der Restore wird in CI getestet.
- **Upgrade:** `docker compose pull && docker compose up -d`; Migrationen laufen automatisch mit Lock; Rollback-Hinweise
  in den Release-Notes.
- **Self-Monitoring:** Der Server überwacht sich selbst: Ingest-Lag, verworfene Samples und die Größe der Datenbank
  gegen `SERMON_DATABASE_SIZE_LIMIT`. Superadmins bekommen bei jedem Wechsel eine Mail und sehen offene Probleme in der
  UI; Organisationen sehen nichts davon ([ADR 0027](adr/0027-server-ereignisse-und-self-monitoring.md)). Den Füllstand der Volumes misst ein Agent auf dem Server-Host.
- **Ressourcen (Richtwert für 50 Nodes):** 4 vCPU, 8 GB RAM, 200 GB NVMe-SSD. Wird im Spike in M0 bestätigt.

---

## 23. Roadmap

Die Aufteilung in Aufgaben mit Abhängigkeiten und Abnahmekriterien steht im
[Implementierungsplan](IMPLEMENTIERUNGSPLAN.md).

| Meilenstein                    | Inhalt                                                                                                                                                                                                                                            | Ergebnis                                        |
|--------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|-------------------------------------------------|
| **M0 – Fundament**             | Monorepo, CI (Lint/Test/Security), `justfile`, Dev-Compose, Proto-Grundgerüst, ADRs D1–D26, Threat Model v1, **Spikes:** TimescaleDB-Ingest mit 50 simulierten Agents (ADR 0001), journald über `journalctl`, musl-Build arm64                                                        | lauffähige Skelette, grüne Pipeline             |
| **M1 – Metriken**              | Enrollment (Token, CSR, mTLS, Rotation), Basis-Collectors (§8.1), WAL-Puffer, Ingest → TimescaleDB, Auth lokal + TOTP, Orgs/Gruppen/Rollen, Fleet-Übersicht, Node-Detail (CPU/Mem/Disk/Net), i18n-Gerüst, Hilfe-Tooltips; **Debian/Ubuntu amd64**          | Erster Node sichtbar in < 2 min ab Installation |
| **M2 – Alerting**              | Alert-Engine, Regeltypen threshold/rate/state/absence/event/prediction, Standardregeln (§12.3 ohne Anomalie), Security-Events (SSH, sudo, Reboot, OOM), alle Kanäle, Routing, Silences, YAML-Import/Export, Alerts-Seite; **RHEL, Alpine, arm64** | SSH-Brute-Force-Alert landet in ntfy/Slack      |
| **M3 – Logs und Live**         | journald/Datei-Logs → Loki, Redaction, Log-Explorer + Live-Tail, Live-Ansicht (top), Security-Dashboard, OIDC, Passkeys, Step-up-Auth                                                                                                             | Fehlersuche ohne SSH möglich                    |
| **M4 – Updates und Anomalien** | Helper, Update-Erfassung, Policies + Rollout, Updates-Dashboard, Agent-Self-Update, Paket-Repos, Anomalie-Erkennung + UI, Kapazitäts-Dashboard, Heatmap                                                                                           | Patch-Compliance auf einen Blick                |
| **M5 – Module und Ausbau**     | Modul-Framework + Docker/nginx/PG/MySQL/Redis/TLS/HTTP-Check/SMART/Prometheus-Scraper, Docker-Logs, eigene Dashboards, Kiosk-Modus, Vergleichsansicht                                                                                             | v1.0-Kandidat                                   |
| **v1.0**                       | Pentest/Review, Doku komplett, Upgrade-/Restore-Tests, Performance-Nachweis (50 Nodes)                                                                                                                                                            | Release                                         |

---

## 24. Offene Punkte und Risiken

| #   | Thema                        | Frage / Risiko                                                                   | Vorschlag                                                                                  |
|-----|------------------------------|----------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------|
| O1  | Last durch 1-s-Auflösung     | ✓ Geklärt (Spike M0-09): 1 s in der DB wäre machbar, erzeugt aber ~320 GB WAL/Tag | Entschieden: 1 s nur im Ringpuffer (15 min), 10 s in der DB (ADR 0001) |
| O2  | Timescale-Lizenz             | Kompression und Continuous Aggregates stehen unter der Timescale License (TSL), nicht Apache-2.0 | Erlaubt für Self-Hosting und SerMon als Produkt bzw. SaaS; verboten wäre nur, TimescaleDB selbst als DBaaS anzubieten. Vor einem SaaS-Start juristisch prüfen |
| O3  | Prozessdetails               | Ohne `CAP_SYS_PTRACE` keine IO/FD-Infos fremder Prozesse                         | Akzeptiert („n/a“). Später ggf. eine feste Helper-Op „ProcessDetails(pid)“ mit Rate-Limit  |
| O4  | Docker-Socket                | root-äquivalent                                                                  | Modul aus + Warnung; cgroup-Variante ohne Socket bevorzugen                                |
| O5  | `curl \| sh`                 | Wird in Security-Kreisen kritisch gesehen                                        | Signaturprüfung im Skript, Option „erst ansehen“, Paket-Repo als gleichwertige Alternative |
| O6  | Alpine-Security-Updates      | apk hat keine Security-Metadaten                                                 | Nur `report`/`all-auto`; ggf. später den secdb-Feed von Alpine auswerten                   |
| O7  | Projekt-/Domainname          | „SerMon“ – Marke/Domain prüfen                                                   | vor dem ersten Release                                                                     |
| O8  | Mandanten in den Metriken    | ✓ Geklärt (Spike M0-09): keine RLS auf Hypertables mit Columnstore und auf Continuous Aggregates | Scope-Pflicht in der Query-Schicht; zusätzlich kein direktes SELECT für `sermon_app`, Lesen nur über `SECURITY DEFINER`-Funktion mit RLS-geschützter `series` (M1-22) |
| O9  | API-Tokens / Terraform       | Automatisierbarkeit (Nodes, Regeln)                                              | Nach v1.0: org-gebundene, gescopte API-Tokens                                              |
| O10 | Mobile                       | Push-Benachrichtigungen reichen?                                                 | ntfy/Pushover/Telegram decken das für v1 ab                                                |
| O11 | HA                           | Kein HA geplant                                                                  | Architektur (PG-Leases, zustandslose Worker) hält es offen                                 |
| O12 | Wachstum über ca. 200 Nodes  | TimescaleDB stößt bei 1-s-Auflösung an Grenzen                                   | `MetricStore` auf VictoriaMetrics umstellen; die Domänenlogik bleibt unverändert (ADR 0001) |
| O13 | Mitglieder hinzufügen        | ✓ Geklärt: Einladungslink statt Startpasswort                                    | Entschieden: D28, in Produktion nur per Mail, SMTP Pflicht (ADR 0008, 0022)                |
| O14 | Orgs löschen                 | ✓ Geklärt: sperren, nach Frist löschen                                           | Entschieden: D29, ADR 0029; umgesetzt mit M2-32 und M2-33                                  |

---

## 25. Agent-Skills (Claude Code)

Unter `.claude/skills/` liegen projektspezifische Skills. Sie halten die Konventionen aus diesem Konzept bei der
Entwicklung mit Claude Code ein.

| Skill                       | Wann                                                                                         |
|-----------------------------|----------------------------------------------------------------------------------------------|
| `sermon-tdd`                | Bei jeder Implementierung von Logik: Red → Green → Refactor für Rust, Go und Svelte          |
| `sermon-security-review`    | Vor jedem PR bzw. Commit mit sicherheitsrelevanten Änderungen; projektspezifische Checkliste |
| `sermon-add-collector`      | Neue Metrik bzw. neuen Collector im Agent hinzufügen (End-to-End bis zur UI)                 |
| `sermon-add-alert-rule`     | Neue Standard-Alert-Regel inklusive Tests und Hilfetext                                      |
| `sermon-add-security-event` | Neuen Security-Event-Parser (Logzeilen → strukturierte Events) mit Fixtures und Fuzzing      |
| `sermon-add-notifier`       | Neuen Benachrichtigungskanal implementieren                                                  |
| `sermon-add-module`         | Neues Integrationsmodul (nginx, PG, …)                                                       |
| `sermon-proto-change`       | Änderungen an `proto/` (Kompatibilität, Codegen)                                             |
| `sermon-ui-component`       | UI-Komponenten und Seiten (Svelte 5, i18n, Hilfe, A11y)                                      |
| `sermon-adr`                | Architekturentscheidung dokumentieren                                                        |
