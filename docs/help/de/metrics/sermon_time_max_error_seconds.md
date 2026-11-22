# Maximaler Zeitfehler

Die Obergrenze des Uhrfehlers, wie der Kernel sie schätzt.

## Einordnung

Solange ein NTP-Dienst die Uhr steuert, bleibt der Wert klein. Ohne Abgleich wächst er jede Sekunde um 0,5 ms bis auf 16 s.

Metrik: `sermon_time_max_error_seconds`
