# Aufbewahrung

Wie lange diese Organisation ihre Logs und Security-Events behält – anders als die Vorgabe der Instanz, etwa kürzer,
weil Logs personenbezogene Daten enthalten, oder Events länger für spätere Aufklärung. Einstellen können es Admins aller Gruppen nach einer frischen Bestätigung; jede
Änderung steht im Audit-Log.

## Grenzen

- **Logs:** 1 bis 14 Tage; leer heißt 14 Tage wie die Instanz. Länger geht nicht, weil Loki nach 14 Tagen alles
  löscht.
- **Security-Events und Alert-Historie:** 7 bis 365 Tage; leer heißt die Vorgabe der Instanz (ab Werk 90 Tage, der
  Betreiber kann sie ändern; der Wert steht unter dem Feld). Unter 7 Tage geht nicht: So lange puffern Agents ihre
  Events, wenn der Server nicht erreichbar ist. Ein Angriff auf SSH erzeugt eine Zeile je Versuch – eine lange Frist
  kostet bei vielen Angriffen viel Platz.
- Hat der Betreiber eine **Obergrenze** für diese Organisation gesetzt, liegt eine eigene Einstellung nicht darüber.
  Eine gesenkte Obergrenze ändert eine bestehende Einstellung nicht.
- Metriken folgen der Aufbewahrung der Instanz (10-s-Werte 24 h, Minuten 30 Tage, Stunden ein Jahr).

## Wann gelöscht wird

- **Events** älter als die Frist löscht der Server stündlich.
- **Logs** löscht Loki: Der Server trägt einmal am Tag eine Löschung aller Zeilen vor der Frist ein, Loki führt sie
  nach etwa 10 Minuten aus. Logs können also bis zu einem Tag länger sichtbar sein.
