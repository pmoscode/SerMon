# Heatmap

Alle Nodes über die Zeit auf einen Blick: Jede Zeile ist ein Node, jede Spalte ein Zeitabschnitt, die Farbe der Wert.
So fallen Muster auf – ein nächtlicher Job auf allen Hosts, ein „Noisy Neighbor“, der immer zur selben Zeit Last macht.

## Bedienung

- **Eingefärbt nach** CPU (Anteil nicht idle), Speicher, IO (die am stärksten ausgelastete Disk des Nodes) oder
  [Anomalie-Rate](help:concepts/anomalies). Alle Werte sind Anteile von 0 bis 100 %.
- Der Zeitraum reicht von 10 Minuten bis 30 Tage. SerMon fasst ihn auf höchstens 200 Spalten zusammen; eine Zelle zeigt
  den höchsten Mittelwert ihres Abschnitts.
- **Legende:** Dunkel heißt wenig, hell viel. Leere Zellen haben keine Daten (der Node war aus oder meldete nichts).
- **Werte:** Zeige auf eine Zelle, oder wähle die Heatmap mit der Tab-Taste und bewege dich mit den Pfeiltasten (Pos1,
  Ende, Bild auf/ab). Die Zeile darüber nennt Node, Zeitabschnitt und Wert.
- **Reihenfolge:** nach Name oder mit dem höchsten bzw. mittleren Wert zuerst.
- **Als Tabelle** zeigt je Node Mittel, höchsten Wert und wann er war – ohne Farben, mit Link auf den Node.

Gezeigt werden höchstens 500 Nodes.

## Typische nächste Schritte

1. Eine helle Spalte über viele Nodes: ein gemeinsamer Job oder ein Ereignis zu dieser Zeit – Logs dieses Zeitraums
   ansehen.
2. Eine helle Zeile: einen Node mit Dauerlast auf seiner Seite untersuchen.
