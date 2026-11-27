# Simulation

Testmodul für Entwicklung und Demos: Es erreicht nichts auf dem Host und verhält sich so, wie es eingestellt ist. Es
gibt das Modul nur in Builds mit dem Feature `simulate` (`just simulate`), nie in einem Release.

## Einstellungen

- **Behavior:** `ok` meldet die Metriken; `fail` meldet einen Fehler (Zustand `error`), `hang` hängt länger als jedes
  Zeitbudget (`timeout`), `panic` stürzt ab (`crashed`) und `flood` schickt mehr Werte, als ein Lauf darf (`error`).
  So lässt sich prüfen, dass die Laufzeit einen fehlerhaften Modul-Lauf eingrenzt und die UI ihn richtig zeigt.
- **Value:** der Wert von `sermon_simulate_value`.
- **Token:** eine Zugangsangabe zum Ausprobieren; das Modul verrät nur, ob sie angekommen ist
  (`sermon_simulate_token`), nie ihren Inhalt.

## Metriken

- [Simulierter Wert](help:metrics/sermon_simulate_value)
- [Simuliertes Geheimnis angekommen](help:metrics/sermon_simulate_token)

## Vorschlag

Virtuelle Nodes aus `just simulate` geben vor, einen Prozess `sermon-simulate` zu haben; der Agent schlägt das Modul
dann vor wie bei einem echten Dienst.
