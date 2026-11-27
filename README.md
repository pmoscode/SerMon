# SerMon

**Schlankes, selbst gehostetes und mandantenfähiges Server-Monitoring für Linux.**
Inspiriert von Netdata, aber bewusst kleiner – mit **Security als oberstem Gebot**.

> ⚠️ **Status: auf dem Weg zu v1.0.** Die Meilensteine M0 bis M5 sind abgeschlossen (bis auf die Prüfung der
> Kanäle gegen die echten Dienste, jetzt V1-14); offen sind Release-Pipeline,
> Testbetrieb, externes Review und Release (Stand: [Implementierungsplan §9](docs/IMPLEMENTIERUNGSPLAN.md#9-v10--release)).
> Bis zum ersten Release baut die Installation das Server-Image aus dem Quellcode. Noch nicht für den produktiven
> Einsatz geeignet.

*A lean, self-hosted, multi-tenant monitoring for Linux servers – security first. Documentation is mostly in German;
help texts, user guide and privacy template are also in English.*

## Was SerMon kann

- **Agent** in Rust, eine statische Binary (amd64, arm64) mit einem Root-Helper für die wenigen privilegierten
  Aufgaben: Installation per Einzeiler oder aus signierten Paket-Repos (.deb, .rpm, .apk), Enrollment mit Token,
  danach mTLS mit Zertifikatsrotation; Puffer auf der Platte, Self-Update mit Rollback. Unter 1 % eines Kerns und
  rund 10 MiB eigener Speicher, auch mit allen Modulen.
- **Metriken** im Sekundentakt (CPU, Speicher, Disk, Netz, Load, Prozesse, systemd-Units, Zeit), Live-Ansicht („top“),
  Flottenübersicht, Node-Seite, Kapazität und Heatmap, Vergleich von 2 bis 4 Nodes.
- **Logs** aus dem systemd-Journal, Logdateien und Containern, mit Redaction vor dem Versand, Log-Explorer und
  Live-Tail (Loki).
- **Alerting** mit Standardregeln, Security-Events (SSH-Brute-Force mit Fleet-Korrelation, Root-Login, sudo, OOM,
  Reboot), Vorhersagen und erklärbarer **Anomalie-Erkennung**; Benachrichtigung per E-Mail, Webhook, ntfy, Slack,
  Teams, Discord, Mattermost, Telegram, Gotify und Pushover mit Policies, Eskalation, Silences und Wartungsfenstern;
  Regeln in der UI und als YAML. Slack, Teams, Discord, Mattermost, Telegram, Gotify und Pushover sind bisher nur
  gegen Test-Server geprüft.
- **Paket-Updates** (apt, dnf, apk): Übersicht der offenen Security-Updates und Rollouts per Policy mit
  Wartungsfenstern, Reboot und Health-Check.
- **Module** für Dienste, ab Werk aus, mit Vorschlag aus der Discovery: HTTP- und TCP-Check, TLS-Zertifikate,
  systemd-Units, nginx, Apache, PostgreSQL, MySQL/MariaDB, Redis, Docker (cgroups und Socket), SMART,
  Prometheus-Scraper – begrenzt durch das lokale Veto des Hosts.
- **Eigene Dashboards** mit Widgets und Variablen, geteilt in der Organisation, **Kiosk-Modus** mit widerrufbaren
  Tokens.
- **Mandanten, Gruppen und Rollen**; Anmeldung lokal mit TOTP und Passkeys oder per OIDC, Step-up für kritische
  Aktionen, Audit-Log mit Hash-Kette; Retention je Organisation, Löschen von Nodes, Organisationen und Konten,
  Export der eigenen Daten.
- **Betrieb:** gehärtete Compose-Datei, verschlüsseltes Backup und Restore, Self-Monitoring.
- Web-UI (SvelteKit) auf Deutsch und Englisch mit Kontexthilfe, ohne externe Ressourcen.

Getestet auf Debian, Ubuntu, Rocky, AlmaLinux, Fedora und Alpine (OpenRC), jeweils amd64 und arm64.

## Dokumentation

| Dokument | Inhalt |
|---|---|
| [Installation](docs/admin/installation.md) | Einrichtung mit docker compose (`deploy/compose/`) |
| [Admin-Doku](docs/admin/) | Installation, Backup und Restore, Betrieb |
| [Backup und Restore](docs/admin/backup.md) | `sermon-server backup` und `restore`, pgBackRest, Loki |
| [Benutzerhandbuch](docs/user/de/README.md) ([English](docs/user/en/README.md)) | Flotte, Alerts, Logs, Updates, Module, Dashboards, Rollen |
| [Datenschutz-Vorlage](docs/privacy/de/README.md) ([English](docs/privacy/en/README.md)) | Verarbeitungsverzeichnis, TOMs, Löschfristen – Vorlage, keine Rechtsberatung |
| [Konzept](docs/KONZEPT.md) | Architektur, Entscheidungen, Security-Konzept, Qualitätsregeln |
| [Implementierungsplan](docs/IMPLEMENTIERUNGSPLAN.md) | Meilensteine, Aufgaben, Abnahmekriterien |
| [ADRs](docs/adr/) | Architekturentscheidungen |
| [Threat Model](docs/security/threat-model.md) | Vertrauensgrenzen, Bedrohungen, Gegenmaßnahmen |
| [Security Policy](SECURITY.md) | Schwachstellen melden |

## Entwicklung

Alternativ: Devcontainer (`.devcontainer/`), dann sind alle Werkzeuge installiert.

Voraussetzungen: [`just`](https://github.com/casey/just), [`lefthook`](https://github.com/evilmartians/lefthook),
[`betterleaks`](https://github.com/betterleaks/betterleaks), Rust (rustup, Version aus `agent/rust-toolchain.toml`),
`zig` und `cargo-zigbuild`, `cargo-deny`, Go, `golangci-lint`, `govulncheck`, Node und pnpm, `buf`, `shellcheck`,
Docker. `just setup` prüft, was fehlt.

```bash
just setup     # prüft Voraussetzungen und installiert die Git-Hooks
just dev       # Entwicklungsumgebung: Datenbank, Loki, Mailpit, ntfy, Server (Hot-Reload) und UI → http://127.0.0.1:5173
just simulate  # virtuelle Nodes gegen `just dev`, mit Szenarien für die Alerts
just lint      # Lint aller Teile
just test      # Unit-Tests; just test-integration, just e2e und just e2e-demo für die größeren Tests
just --list    # alle Befehle
```

Beiträge: Commits folgen [Conventional Commits](https://www.conventionalcommits.org/), Arbeit erfolgt testgetrieben (TDD).
Details in [KONZEPT §19](docs/KONZEPT.md#19-qualität-clean-code-tdd-und-prozess) und in [CLAUDE.md](CLAUDE.md).

## Lizenz

[GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0).
