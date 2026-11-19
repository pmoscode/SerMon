# Load pro CPU

Die durchschnittliche Zahl der Prozesse, die in der letzten Minute rechnen wollten oder auf Datenträger warteten – geteilt durch die Zahl der CPU-Kerne.

## Einordnung

- Um 1,0 ist jeder Kern ausgelastet; darunter gibt es Reserven.
- Deutlich über 1,0 warten Prozesse: Der Server reagiert langsamer.
- Eine hohe Load bei niedriger CPU-Auslastung deutet meist auf langsame Datenträger oder Netzlaufwerke hin (Prozesse im Zustand „D“).

## Nächste Schritte

1. Im Node-Detail die Load über 1, 5 und 15 Minuten vergleichen: Steigt sie oder klingt sie ab?
2. CPU-Auslastung und iowait gegenüberstellen.
