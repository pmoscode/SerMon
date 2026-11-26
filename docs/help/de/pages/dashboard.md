# Dashboard

Ein eigenes Dashboard: Widgets in einem Raster aus 12 Spalten. Alle Charts folgen dem Zeitraum oben; Zoom in einem
Chart zoomt alle.

## Widgets

- **Zeitreihe** – eine Metrik über den Zeitraum, eine Linie je Node und Serie.
- **Gauge** und **Kennzahl** – der letzte Wert einer Metrik eines Nodes.
- **Tabelle** – der letzte Wert einer Metrik je Node und Serie.
- **Log-Stream** – die neuesten Log-Zeilen, auf Wunsch nur mit einem Text oder Schweregrad.
- **Alert-Liste** – die aktiven Alerts der Nodes.

Jedes Widget liest mit deinen Rechten: Nodes, die du nicht sehen darfst, bleiben leer. Laufende Zeiträume werden alle
30 Sekunden aktualisiert; schlägt das fehl, behält ein Widget seine letzten Daten und sagt, dass sie nicht aktuell sind.

## Variablen

Mit einer Variablen wählst du über den Widgets einen Node oder eine Gruppe; Widgets, die sich darauf beziehen, folgen
der Auswahl. Die Auswahl steht in der Adresse, ein Link öffnet also dieselbe Ansicht. Ohne Auswahl gilt die Vorgabe,
sonst der erste Node bzw. die erste Gruppe, die du siehst. Widgets mit Variablen statt fester Nodes funktionieren auch
nach einem Import in eine andere Organisation.

## Bearbeiten

- **Bearbeiten** öffnet den Editor: Widgets hinzufügen, mit dem Stift ändern, mit dem Kreuz entfernen.
- **Verschieben:** am Griff ziehen, oder ihn mit der Tab-Taste fokussieren und die Pfeiltasten nutzen. **Größe:** an der
  Ecke ziehen, oder Umschalt und Pfeiltasten. Widgets im Weg rutschen nach unten. Entf entfernt das fokussierte Widget.
- **Speichern** legt das Dashboard ab. Hat es inzwischen jemand geändert, lade es neu und wiederhole deine Änderung.
- **Teilen** zeigt das Dashboard der Organisation; **Export** speichert es als JSON für einen Import anderswo.

Höchstens 30 Widgets, 20 Nodes je Widget und 4 Variablen.
