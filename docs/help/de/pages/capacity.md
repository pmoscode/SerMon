# Kapazität

Wo der Platz knapp wird und welche Nodes mehr CPU und Speicher brauchen als vor einem Monat.

## Dateisysteme nach Prognose

Alle Dateisysteme der Fleet, die zuerst vollen oben: SerMon legt eine Gerade durch den freien Platz der letzten 24
Stunden und sagt, wann sie null erreicht ([Prognose](help:metrics/sermon_filesystem_avail_bytes)). Ohne fallenden Trend
gibt es keine Prognose; diese Dateisysteme folgen nach Belegung. Schreibgeschützte bekommen keine. Gezeigt werden höchstens
500.

## CPU und Speicher über 30 Tage

Je Node das Mittel der letzten 7 Tage und die Änderung gegenüber den ersten 7 der 30 Tage, in Prozentpunkten (aus
+10 % wird bei 40 % → 50 %). Der Verlauf zeigt die Tagesmittel. **Nach CPU** und **Nach Speicher** sortieren die
größten Verbraucher nach oben. Ein Klick auf den Node öffnet seine Seite mit den Charts.

## Typische nächste Schritte

1. Ein Dateisystem mit naher Prognose aufräumen oder vergrößern; die Standardregel warnt ebenfalls.
2. Einen Node mit stark gestiegener Last prüfen: neue Dienste, ein Leck, mehr Nutzer?
