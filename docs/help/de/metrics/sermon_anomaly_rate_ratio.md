# Anomalie-Rate

Welcher Anteil der bewerteten Metriken des Nodes gerade ungewöhnlich ist (0 bis 100 %), jede Minute neu berechnet.

## Einordnung

Eine Metrik ist ungewöhnlich, wenn sie zugleich weit außerhalb dessen liegt, was zu dieser Stunde der Woche üblich ist,
und weit weg vom Verlauf der letzten Stunden – drei von fünf Minuten lang. Normal sind 0 % oder kurze, kleine Werte. Ab
10 % (kritisch: 25 %) warnt die Standardregel. In den ersten 7 Tagen lernt SerMon den Node kennen; die Rate bleibt dann 0.

Metrik: `sermon_anomaly_rate_ratio`
