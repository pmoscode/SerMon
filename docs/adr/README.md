# Architecture Decision Records

Entscheidungen mit Tragweite für die Architektur, im Format aus dem Skill `sermon-adr`. Ein ADR wird nie gelöscht:
Wird es ersetzt, bekommt es den Status `superseded by NNNN`. Ändert ein ADR das Konzept, wird das Entscheidungslog in
KONZEPT §2 mit angepasst.

## Übersicht

| Nr. | Titel | Status | Entscheidungen |
|---|---|---|---|
| [0001](0001-zeitreihen-speicher-timescaledb.md) | Zeitreihen-Speicher: TimescaleDB statt VictoriaMetrics | accepted | D13 (D6, D12) |
| [0002](0002-agent-build-toolchain.md) | Build-Toolchain für den Agent: cargo-zigbuild | accepted | D1, D7 |
| [0003](0003-ui-auslieferung-und-csp.md) | UI-Auslieferung: eingebettete SPA, strikte CSP, keine externen Ressourcen | accepted | D3, D27 |
| [0004](0004-protokoll-codegenerierung.md) | Protokoll: buf mit lokalen Plugins, generierter Code im Repo | accepted | D26 |
| [0005](0005-secret-scanning-betterleaks.md) | Secret-Scanning: betterleaks statt gitleaks | accepted | D25 |
| [0006](0006-ci-und-supply-chain.md) | CI-Pipeline und Supply Chain der Build-Werkzeuge | accepted | D25 |
| [0007](0007-mandantentrennung-rls.md) | Mandantentrennung in PostgreSQL: Tenant-Scope, RLS und getrennte Rollen | accepted | D5 |
| [0008](0008-einladungen.md) | Mitglieder per Einladungslink, SMTP optional | accepted, Zustellung ersetzt durch 0022 | D28 |
| [0009](0009-enrollment-ueber-web-endpunkt.md) | Enrollment über den Web-Endpunkt, danach mTLS | accepted | D30 |
| [0010](0010-agent-enrollment-tls-und-identitaet.md) | Agent-Enrollment: TLS-Prüfung, Pinning und Identitätsdateien | accepted | D8, D30 |
| [0011](0011-server-metriken-endpunkt.md) | Eigene Metriken des Servers: `/metrics` auf eigenem Listener | accepted | – |
| [0012](0012-agent-installation-und-signaturen.md) | Agent-Installation: Skript, Signaturen und gehärtete Unit | accepted | D8, D9 |
| [0013](0013-log-quelle-journald.md) | Log-Quelle journald: `journalctl` als Kindprozess, eigener Cursor | accepted | D17 |
| [0014](0014-technologie-stack.md) | Technologie-Stack: Rust-Agent, Go-Server, SvelteKit-UI, Connect-RPC | accepted | D1, D2, D3, D26 |
| [0015](0015-topologie-und-betriebsmodell.md) | Topologie und Betriebsmodell: Push mit Puffer, self-hosted, mandantenfähig, docker-compose | accepted | D4, D5, D6, D23 |
| [0016](0016-zielsysteme-und-agent-rechte.md) | Zielsysteme und Agent-Rechte: Distributionen, Einzeiler, Privilegientrennung, Live-Ansicht nur lesend | accepted | D7, D8, D9, D19 |
| [0017](0017-authentifizierung-und-rechte.md) | Authentifizierung und Rechte: lokale Konten mit 2FA, OIDC, Rollen pro Node-Gruppe | accepted | D10, D11 |
| [0018](0018-retention.md) | Retention: gestaffelte Auflösung für Metriken, Logs und Events | accepted | D12 |
| [0019](0019-alerting.md) | Alerting: Kanäle, statistische Anomalien, Regeln in UI und YAML | accepted | D14, D16, D22 |
| [0020](0020-updates-logs-und-module.md) | Updates, Logs und Module | accepted | D15, D17, D18 |
| [0021](0021-produkt-und-projekt.md) | Produkt und Projekt: Sprachen, Lizenz, Reihenfolge, Monorepo | accepted | D20, D21, D24, D25 |
| [0022](0022-betriebsmodus-und-einladungen-per-mail.md) | Betriebsmodus und Einladungen nur per Mail | accepted | D28 |
| [0023](0023-vertrauen-in-security-events.md) | Vertrauen in Security-Events: nur von privilegierten Absendern | accepted | – |
| [0024](0024-ausgehende-verbindungen-safehttp.md) | Ausgehende Verbindungen: `safehttp`, Allowlist in der Server-Konfiguration | accepted | – |
| [0025](0025-logdateien-ohne-journald.md) | Logdateien als Quelle ohne journald: auswerten, aber markieren | accepted | D7, D17 |
| [0026](0026-zustands-collectors.md) | Zustands-Collectors: adjtimex, systemctl und erholte Units | accepted | D9 |
| [0027](0027-server-ereignisse-und-self-monitoring.md) | Ereignisse des Servers als Security-Events, Self-Monitoring für Superadmins | accepted | D14 |
| [0028](0028-produktions-compose.md) | Produktions-Compose: Agent-Port direkt, Server-Image aus dem Quellcode | accepted | D23 |
| [0029](0029-organisationen-verwalten-sperren-loeschen.md) | Organisationen verwalten, sperren und löschen | accepted | D29 |
| [0030](0030-konfiguration-vom-server.md) | Konfiguration vom Server an die Agents | accepted | D17 |
| [0031](0031-logs-in-loki.md) | Logs in Loki: fester Endpunkt, eigener Client, sparsame Labels | accepted | D13, D17 |
| [0032](0032-live-tail-durch-abfragen.md) | Live-Tail durch wiederholte Abfragen statt Loki-WebSocket | accepted | D13 |
| [0033](0033-anmeldung-ueber-oidc.md) | Anmeldung über OpenID Connect | accepted | D10 |
| [0034](0034-passkeys-mit-webauthn.md) | Passkeys mit WebAuthn | accepted | D10 |
| [0035](0035-root-helper.md) | Root-Helper: Protokoll, Operationen und Läufe | accepted | D9, D15 |
| [0036](0036-self-update-und-paket-repos.md) | Self-Update des Agents und signierte Paket-Repos | accepted | D8, D15 |
| [0037](0037-anomalie-erkennung.md) | Anomalie-Erkennung: Baselines, Ergebnisse und Worker | accepted | D16, D13 |
| [0038](0038-allocator-des-agents.md) | Allocator des Agents: jemalloc statt musls mallocng | accepted | D1 |
| [0039](0039-modul-framework.md) | Modul-Framework: Laufzeit, Konfiguration, Veto und Zugangsdaten | accepted | D18 |
| [0040](0040-eigene-dashboards.md) | Eigene Dashboards: Modell, Rechte, Grid-Editor und Kiosk-Token | accepted | D3, D11, D27 |
| [0041](0041-nodes-deaktivieren-loeschen.md) | Nodes deaktivieren und löschen | accepted | D29, D31 |
| [0042](0042-logisches-backup-und-restore.md) | Logisches Backup und Restore | accepted | D32 |
| [0043](0043-release-prozess.md) | Release-Prozess: Versionen, Kanäle, Schlüssel und Supply Chain | accepted | D33 |
| [0044](0044-log-zaehlungen-fuer-das-histogramm.md) | Zählungen der Log-Zeilen für das Histogramm | accepted | D13, D12 |

## Entscheidungslog → ADR

| Entscheidung | ADR |
|---|---|
| D1 Agent in Rust | 0014, 0002, 0038 |
| D2 Server in Go | 0014 |
| D3 SvelteKit-SPA | 0014, 0003, 0040 |
| D4 Push mit Puffer | 0015 |
| D5 Self-hosted, mandantenfähig | 0015, 0007 |
| D6 < 50 Nodes | 0015, 0001 |
| D7 Zielsysteme | 0016, 0002 |
| D8 Einzeiler mit Token | 0016, 0012 |
| D9 Privilegientrennung | 0016, 0012 |
| D10 Authentifizierung | 0017, 0033, 0034 |
| D11 Rechte | 0017, 0040 |
| D12 Retention | 0018 |
| D13 Storage | 0001, 0031, 0032, 0037, 0044 |
| D14 Alert-Kanäle | 0019 |
| D15 Updates | 0020, 0035, 0036 |
| D16 Anomalien | 0019, 0037 |
| D17 Logs | 0020, 0013 |
| D18 Module | 0020, 0039 |
| D19 Live-Ansicht nur lesend | 0016 |
| D20 DE + EN | 0021 |
| D21 AGPL-3.0 | 0021 |
| D22 Regeln in UI und YAML | 0019 |
| D23 docker-compose | 0015, 0028 |
| D24 Reihenfolge | 0021 |
| D25 Monorepo, GitHub Actions | 0021, 0006 |
| D26 Connect-RPC/Protobuf | 0014, 0004 |
| D27 Alles lokal ausgeliefert | 0003 |
| D28 Einladungslink | 0008, 0022 |
| D29 Orgs erst sperren, dann löschen | 0029 |
| D30 Enrollment über HTTPS | 0009, 0010 |
| D31 Nodes erst deaktivieren, dann löschen | 0041 |
| D32 Logisches Backup, verschlüsselt mit age | 0042 |
| D33 Release über GHCR, Schlüssel in GitHub-Environment | 0043 |

Die ADRs 0014 bis 0021 dokumentieren Entscheidungen der Konzeptrunde nachträglich (IMPLEMENTIERUNGSPLAN M0-11).
