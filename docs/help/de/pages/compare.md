# Vergleich

Zwei bis vier Nodes im selben Zeitraum: vor und nach einer Änderung, Mitglieder eines Clusters oder ein auffälliger Host
neben einem unauffälligen.

## Bedienung

- **Nodes wählen:** Über „Node hinzufügen“ kommen bis zu vier Nodes dazu, das ✕ entfernt einen. Die Auswahl steht in der
  Adresse der Seite – ein Link zeigt anderen denselben Vergleich (sie sehen nur Nodes, für die sie Rechte haben). Die
  Node-Seite führt mit „Vergleichen“ hierher.
- **Nebeneinander:** je Metrik ein Chart pro Node, alle mit derselben Skala, damit gleiche Höhe gleichen Wert bedeutet.
- **Übereinander:** je Metrik ein Chart mit einer Linie pro Node.
- **Zeitraum:** gilt für alle Charts. Ziehen in einem Chart zoomt alle, ein Doppelklick setzt zurück; der Crosshair
  zeigt in allen Charts denselben Zeitpunkt.
- **Metriken:** CPU-Auslastung (Anteil nicht idle), Speicher belegt, Last je CPU (1 min), die Auslastung der am stärksten
  belasteten Disk und der Netzverkehr aller Interfaces zusammen.
- **Keine Daten:** Ein Node ohne Werte im Zeitraum (aus, neu oder nicht erreichbar) wird über den Charts genannt. Ein
  unbekannter Node in der Adresse (gelöscht oder aus einer anderen Org) wird nicht abgefragt.

## Typische nächste Schritte

1. Ein Node weicht ab: auf seiner Seite die Details und die Logs um den Zeitpunkt ansehen.
2. Alle Nodes zeigen denselben Ausschlag: ein gemeinsamer Auslöser (Job, Deployment, Last von außen) – die
   [Heatmap](help:pages/heatmap) zeigt, ob die ganze Fleet betroffen war.
