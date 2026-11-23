# Abgelehnte Logdateien

Dateien, auf die ein Datei-Muster passt, die der Agent aber nicht lesen darf.

## Einordnung

- Der Agent liest nur unter den Verzeichnissen aus `log_file_prefixes` in seiner `agent.toml` (Standard `/var/log`). Nie liest er sein State-Verzeichnis, `/etc/sermon`, `/proc`, `/sys` oder `/dev` – auch nicht über einen Symlink. So kann auch ein kompromittierter Server keine Schlüssel des Agents auslesen.
- Mehr als 64 passende Dateien zählen hier ebenfalls mit.
- Muster, die schon mit ihrem festen Anfang außerhalb liegen (z. B. `/etc/*`), sucht der Agent gar nicht erst ab; sie stehen im Log des Agents.

## Nächste Schritte

1. Das Muster prüfen: Zeigt es auf einen Symlink nach außen?
2. Soll der Agent wirklich dort lesen, das Verzeichnis auf dem Host in `log_file_prefixes` eintragen und den Agent neu starten.

Metrik: `sermon_agent_log_files_rejected`
