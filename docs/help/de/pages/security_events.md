# Security

Was auf den Nodes an der Anmeldung passiert: fehlgeschlagene und erfolgreiche SSH-Logins, Root-Logins, sudo-Fehler und weitere Security-Events, mit Filtern und Export als CSV.

## So liest du die Seite

- Der **Zeitraum** gilt für alles auf der Seite. Im Chart ziehen zoomt hinein; die Events werden ab Werk 90 Tage aufbewahrt, wenn die Organisation nichts anderes einstellt ([Aufbewahrung](help:concepts/retention)).
- Die **Summen** oben zählen den Zeitraum. Ein Klick auf „… zeigen“ filtert die Tabelle auf genau diese Events.
- Der **Chart** stapelt die fehlgeschlagenen Logins nach Node; die sechs Nodes mit den meisten haben eine eigene Farbe, die übrigen teilen sich eine. Die Legende nennt jede Farbe mit ihrer Zahl.
- **Top-IPs und Top-User** sind die Adressen und User-Namen mit den meisten Fehlversuchen. Ein Klick filtert die Tabelle darauf. „(leer)“ ist ein leerer User-Name, den ein Angreifer probiert hat.
- Die **Tabelle** zeigt die neuesten Events zuerst. „neu“ heißt: Der User hat sich zum ersten Mal von dieser Quelle auf dem Node angemeldet. „unverifiziert“ heißt: Die Zeile stammt aus einer Logdatei, die jeder lokale User schreiben kann – mehr unter [nicht verifizierte Quelle](help:concepts/unverified-source).
- „Logs“ in einer Zeile öffnet die Logs des Nodes 15 Minuten vor und nach dem Event.
- **Als CSV exportieren** speichert die Events des Filters, höchstens 10 000. Werte, die eine Tabellenkalkulation als Formel lesen würde, beginnen mit einem Apostroph.

## Typische nächste Schritte

1. Eine auffällige Adresse in den Top-IPs anklicken und prüfen, auf welchen Nodes sie es versucht.
2. Bei einem Login von einer neuen Quelle mit der Person klären, ob er von ihr kam.
3. Die Logs um ein Event öffnen, um zu sehen, was danach in der Sitzung geschah.
