# TCP-Check

Prüft, ob TCP-Ports Verbindungen annehmen und wie schnell. Das Modul ist ab Werk aus; bis zu 16 Ziele werden
gleichzeitig geprüft, gesendet wird nichts.

## Voraussetzungen

Nur Ziele unter `module_targets` in `[local]` von `/etc/sermon/agent.toml` (ab Werk Loopback), Namen unter
`module_hosts` (ab Werk `localhost`) und Ports unter `module_ports` (ab Werk die der Dienste der Module). Ein Name wird
einmal aufgelöst, jede Adresse muss erlaubt sein.

## Einstellungen

- **Ziele:** `host:port`, für IPv6 `[adresse]:port`.
- **Timeout:** je Verbindung, höchstens 25 s.

## Metriken

- `sermon_tcp_check_up`: 1, wenn eine Verbindung zustande kam.
- `sermon_tcp_check_duration_seconds`: Zeit bis zur Verbindung.

## Wenn es nicht läuft

- Zustand `vetoed`: Das Ziel liegt außerhalb von `module_targets`, `module_hosts` oder `module_ports`.
