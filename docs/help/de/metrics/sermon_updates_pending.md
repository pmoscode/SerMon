# Verfügbare Updates

Wie viele Pakete des Nodes sich laut seinem letzten Bericht aktualisieren lassen, Security-Updates eingeschlossen.

## Einordnung

Der Agent listet die Updates alle 6 Stunden, nach jedem Lauf des Paketmanagers und auf Anforderung; der Server schreibt
die Zahl dazwischen weiter. Ohne Root-Helper oder wenn das Auflisten scheitert, fehlt die Serie. Viele Updates sind
für sich kein Problem; wichtig sind die [Security-Updates](help:metrics/sermon_updates_security_pending).

Metrik: `sermon_updates_pending`
