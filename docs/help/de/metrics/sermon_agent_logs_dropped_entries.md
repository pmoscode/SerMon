# Verworfene Log-Einträge

Log-Einträge, die der Agent seit seinem Start nicht gesendet hat, nach Grund:

- `rate`: Der Host schrieb mehr als 500 Einträge oder 512 KiB pro Sekunde (über eine Spitze hinaus).
- `buffer`: Logs belegten schon die Hälfte des Puffers, weil der Server sie länger nicht angenommen hat. Metriken und
  Security-Events behalten ihren Platz.

## Einordnung

- 0 ist der Normalfall.
- `rate` deutet auf einen Dienst, der sehr viel loggt: In den Log-Einstellungen der Gruppe lässt er sich ausschließen,
  oder die Mindest-Severity wird angehoben.
- `buffer` heißt meist, dass der Server oder die Verbindung länger nicht erreichbar war.

Metrik: `sermon_agent_logs_dropped_entries`
