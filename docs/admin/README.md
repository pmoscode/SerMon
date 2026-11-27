# Admin-Doku / Admin guide

Für Betreiber einer SerMon-Instanz: Server einrichten, absichern, betreiben und Fehler suchen.
For operators of a SerMon instance: set up, secure and run the server, and find faults.

## Deutsch

- [Installation mit docker compose](installation.md) – Einrichten, Agents anbinden, Paket-Repos, Root-Helper,
  Aktualisieren, OIDC
- [Backup und Restore](backup.md) – Sichern mit `sermon-server backup`, Wiederherstellen, pgBackRest, Loki
- [Releases machen](../release.md) – für den Product Owner: Schlüssel, Environment `release`, Release-PR, Freigabe,
  Schlüsselwechsel; ein Release prüfen mit `scripts/verify-release.sh`
- [Härtung](de/hardening.md) – Vetos in `agent.toml` und `helper.toml`, `install.sh`, systemd, Netze, ein anderer
  Reverse-Proxy, Secrets, Anmeldung, Zugangsdaten der Module, was ein übernommener Server kann
- [Betrieb](de/operations.md) – Self-Monitoring, Aufbewahrung, Löschen, Enrollment-Tokens, Updates, Zertifikate,
  Kapazität
- [Fehlersuche](de/troubleshooting.md) – Agent verbindet sich nicht, fehlende Metriken und Logs, Module, Helper und
  Updates, Alerts, Step-up, Migrationen
- [Konfiguration des Servers](de/configuration.md) – jede Variable `SERMON_*` mit Voreinstellung
- [Lasttest und Performance-Nachweis](de/load-test.md) – `just load-test`, Messung einer laufenden Installation mit
  `scripts/load-report.sh`, Testbetrieb
- [Vorlage zur DSGVO-Dokumentation](../privacy/de/README.md)
- [Benutzerhandbuch](../user/de/README.md) und [Threat Model](../security/threat-model.md)

## English

- [Installation with docker compose](installation.md), [Backup and restore](backup.md) and
  [making releases](../release.md) (in German)
- [Hardening](en/hardening.md) – vetoes in `agent.toml` and `helper.toml`, `install.sh`, systemd, networks, another
  reverse proxy, secrets, sign-in, module credentials, what a compromised server can do
- [Operations](en/operations.md) – self-monitoring, retention, deletion, enrollment tokens, updates, certificates,
  capacity
- [Troubleshooting](en/troubleshooting.md) – agent doesn't connect, missing metrics and logs, modules, helper and
  updates, alerts, step-up, migrations
- [Server configuration](en/configuration.md) – every `SERMON_*` variable with its default
- [Load test and proof of performance](en/load-test.md) – `just load-test`, measuring a running installation with
  `scripts/load-report.sh`, test operation
- [GDPR documentation template](../privacy/en/README.md)
- [User guide](../user/en/README.md) and [threat model](../security/threat-model.md) (in German)

Die Angaben entsprechen dem Code: `server/cmd/sermon-server/admin_docs_test.go` verlangt jede Variable des Servers,
jeden Schlüssel von `[local]` und `helper.toml` und jede Option von `install.sh` in beiden Sprachen, prüft die
Voreinstellungen gegen den Code und alle Links. The statements match the code; that test fails when a variable, veto
or option is added without documentation.
