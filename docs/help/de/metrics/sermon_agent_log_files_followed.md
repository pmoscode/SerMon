# Gefolgte Logdateien

Logdateien, die der Agent gerade liest, weil ein Datei-Muster in den Log-Einstellungen der Gruppe sie nennt (z. B. `/var/log/nginx/*.log`).

## Einordnung

- Höchstens 64 Dateien pro Node. Rotierte und komprimierte Dateien (`access.log.1`, `.gz`) zählen nicht mit: Ihre Zeilen kamen schon unter dem aktiven Namen.
- Neue Dateien findet der Agent innerhalb von 10 Sekunden; eine neue Datei liest er von Anfang an.
- 0 trotz Mustern heißt: Keine passende Datei existiert, der Versand von Logs ist aus, oder die Dateien liegen außerhalb der erlaubten Verzeichnisse (siehe [abgelehnte Logdateien](help:metrics/sermon_agent_log_files_rejected)).

Metrik: `sermon_agent_log_files_followed`
