# Flotte

Die Übersicht aller Server, die du in deinen Node-Gruppen sehen darfst – mit Status und den wichtigsten Kennzahlen auf einen Blick.

## So liest du die Seite

- **Online** heißt: Der Agent hat sich in den letzten 60 Sekunden gemeldet. Ein Server ohne Kontakt erscheint als **offline**, auch wenn er nur das Netz verloren hat.
- Die Kennzahlen zeigen den letzten Wert der vergangenen Minute: [CPU-Auslastung](help:figures/cpu_busy), [Arbeitsspeicher](help:figures/memory_used), [vollstes Dateisystem](help:figures/filesystem_max_used), [Load pro CPU](help:figures/load_per_cpu), [Uptime](help:figures/uptime) und [Netzwerk](help:figures/network).
- Fehlt eine Kennzahl, hat der Agent sie noch nicht geliefert, etwa kurz nach dem Start.
- [Tags](help:concepts/tags) wie `env=prod` stehen unter dem Namen; der Filter **Tag** zeigt nur Server mit diesem Paar, die Suche findet Tags ebenfalls.

## Typische nächste Schritte

1. Offline-Server prüfen: Läuft der Dienst `sermon-agent`, ist der Server erreichbar?
2. Auffällige Werte anklicken und im Node-Detail den Verlauf ansehen. Wie die Charts funktionieren, steht unter [Charts und Zeitraum](help:concepts/charts).
3. Neue Server über „Node hinzufügen“ einbinden.
