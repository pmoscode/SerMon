# SerMon

**Schlankes, selbst gehostetes und mandantenfähiges Server-Monitoring für Linux.**
Inspiriert von Netdata, aber bewusst kleiner – mit **Security als oberstem Gebot**.

> ⚠️ **Status: in früher Entwicklung (Meilenstein M2).** Die Server-Seite für Anmeldung, Mandanten, Enrollment und die
> mTLS-Agent-API steht. Der Agent enrollt sich, sammelt die Basis-Metriken und liefert sie über Puffer, Reconnect und
> Zertifikatsrotation an den Server, der sie in TimescaleDB speichert; die Abfrage-API für die UI steht.
> Die UI hat Login, Setup-Assistent und App-Shell; Charts und Flottenübersicht folgen.
> Die Alert-Engine wertet Standardregeln für Metriken und Security-Events aus (SSH-Brute-Force, Fleet-Korrelation,
> Root-Login, sudo, OOM, Reboot). Benachrichtigungen per E-Mail, Webhook, ntfy, Slack, Teams,
> Discord, Mattermost, Telegram, Gotify und Pushover sind angebunden; Notification-Policies leiten die Alerts mit
> Wiederholung und Eskalation an die Kanäle; Silences und Wartungsfenster schalten sie stumm. Die Alert-Seite zeigt
> die aktiven Alerts mit Verlauf, Quittieren, Silences und dem Regel-Editor (YAML-Import und -Export); Kanäle,
> Policies und Wartungsfenster verwalten Admins in der UI; Tags wie `env=prod` an Nodes dienen als Filter und für das
> Matching von Regeln, Policies und Silences. Getestet auf Debian, Ubuntu, Rocky, AlmaLinux, Fedora und
> Alpine (OpenRC; ohne journald liest der Agent Logdateien und markiert Events daraus als nicht verifiziert), jeweils
> amd64 und arm64; neben dem Einzeiler gibt es signierte Pakete (.deb, .rpm, .apk). Für eine Installation gibt es
> eine gehärtete Compose-Datei ([Anleitung](docs/admin/installation.md)). Zum Ausprobieren der Alerts spielt der
> Simulator Szenarien durch: CPU-Last, volle Platte, SSH-Brute-Force, Flattern und Ausfall (`just simulate`).
> Noch nicht für den produktiven Einsatz geeignet.
> Stand der Aufgaben: [Implementierungsplan](docs/IMPLEMENTIERUNGSPLAN.md).

*A lean, self-hosted, multi-tenant monitoring for Linux servers – security first. Documentation is in German.*

## Geplante Features

- **Zero-Config-Agent** (Rust, eine statische Binary): Installation per Einzeiler mit Enrollment-Token, mTLS zum Server
- **Metriken** im Sekundentakt: CPU, Memory, Disk-IO, Netzwerk, Load, Uptime – mit Detailansichten und Live-Ansicht („top“)
- **Logs**: systemd-journal, klassische Log-Dateien, Docker
- **Alerting** mit Standardregeln (CPU, RAM, Disk, Netz, Reboot, Updates) und **Security-Events** wie fehlgeschlagenen SSH-Logins;
  Benachrichtigung per E-Mail, Webhook, Slack, Teams, Discord, Mattermost, Telegram, ntfy, Gotify, Pushover
- **Anomalie-Erkennung** (statistisch, erklärbar)
- **Paket-Updates** per Policy (apt, dnf, apk) mit Wartungsfenstern
- **Mandanten, Gruppen und Rollen**; Anmeldung lokal mit TOTP/Passkeys oder per OIDC
- Moderne Web-UI (SvelteKit) auf Deutsch und Englisch, mit Kontexthilfe

Unterstützte Systeme: Debian, Ubuntu, RHEL/Rocky/Alma/Fedora, Alpine – jeweils amd64 und arm64.

## Dokumentation

| Dokument | Inhalt |
|---|---|
| [Konzept](docs/KONZEPT.md) | Architektur, Entscheidungen, Security-Konzept, Qualitätsregeln |
| [Implementierungsplan](docs/IMPLEMENTIERUNGSPLAN.md) | Meilensteine, Aufgaben, Abnahmekriterien |
| [ADRs](docs/adr/) | Architekturentscheidungen |
| [Installation](docs/admin/installation.md) | Einrichtung mit docker compose (`deploy/compose/`) |
| [Security Policy](SECURITY.md) | Schwachstellen melden |

## Entwicklung

Alternativ: Devcontainer (`.devcontainer/`), dann sind alle Werkzeuge installiert.

Voraussetzungen: [`just`](https://github.com/casey/just), [`lefthook`](https://github.com/evilmartians/lefthook),
[`betterleaks`](https://github.com/betterleaks/betterleaks), `shellcheck` – später zusätzlich Rust, Go, Node + pnpm, `buf`, Docker.

```bash
just setup   # prüft Voraussetzungen und installiert die Git-Hooks
just dev     # Entwicklungsumgebung: Datenbank, Loki, Mailpit, ntfy, Server (Hot-Reload) und UI → http://127.0.0.1:5173
just --list  # alle Befehle
just lint    # Lint
just test    # Tests
```

Beiträge: Commits folgen [Conventional Commits](https://www.conventionalcommits.org/), Arbeit erfolgt testgetrieben (TDD).
Details in [KONZEPT §19](docs/KONZEPT.md#19-qualität-clean-code-tdd-und-prozess).

## Lizenz

[GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0).
