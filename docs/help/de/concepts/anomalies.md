# Anomalien

SerMon erkennt ungewöhnliche Werte ohne feste Schwellen: Jede Minute vergleicht es rund 30 Kernmetriken je Node mit
dem, was zur selben Stunde der Woche in den letzten vier Wochen üblich war, und mit dem Verlauf der letzten Stunden.

## Score und Band

- Der **Score** sagt, wie weit ein Wert außerhalb des Üblichen liegt, gemessen in robusten Standardabweichungen. Ab 3
  ist er bemerkenswert, ab 4 auffällig.
- **Anomal** ist eine Minute erst, wenn beide Vergleiche über 4 liegen und das in 3 von 5 Minuten gilt; einzelne
  Ausreißer zählen so nicht.
- Das **rote Band** in den Charts markiert diese Minuten, neben dem Titel steht dann „ungewöhnlich“.
- **Was war ungewöhnlich?** listet für einen Zeitraum die Metriken mit dem höchsten Score. Markiere den Zeitraum, indem
  du in einem Chart ziehst; ein Doppelklick setzt ihn zurück.
- Was regelmäßig vorkommt, etwa ein nächtliches Backup von wenigen Minuten, gilt zu seiner Stunde als üblich.

## Lernphase

Neue Nodes und neue Metriken lernen 7 Tage, bevor SerMon sie bewertet: Erst dann gibt es für jede Stunde der Woche eine
Vergleichsbasis. Bis dahin zeigt die Seite des Nodes „lernt noch (Tag 3/7)“, es gibt weder Band noch Alerts.

## Anomalie-Rate

Der Anteil der bewerteten Metriken eines Nodes, die gerade anomal sind. Die Standardregel warnt ab 10 % und wird ab
25 % kritisch.
