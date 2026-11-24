# Node

Alles zu einem Server: Kopfzeile mit Host-Fakten und aktuellen Kennzahlen, darunter Charts nach Bereichen (CPU, Speicher, Datenträger, Netzwerk, Prozesse, Logs, Security, Identität).

## So liest du die Seite

- Alle Charts zeigen denselben Zeitraum und folgen gemeinsam dem Mauszeiger. Ziehen zoomt, Doppelklick setzt zurück – mehr unter [Charts und Zeitraum](help:concepts/charts).
- Jeder Chart hat ein ⓘ mit Erklärung der Metrik.
- Die Tabelle der Dateisysteme zeigt, wann ein Dateisystem bei gleichem Trend voll ist (lineare Regression über 24 Stunden). „Nicht absehbar“ heißt: kein Wachstum oder erst in über einem Jahr.
- Im Bereich Netzwerk lassen sich die Raten in bit/s statt Byte/s anzeigen.
- Ein Hinweis unter dem Namen erscheint, wenn der Node keine Security-Events liefern kann oder sie nur unverifiziert aus einer Logdatei kommen – mehr unter [Quelle der Security-Events](help:concepts/security-source).
- Der Reiter **Prozesse (Live)** zeigt die Prozesse wie in `top`, alle 2 Sekunden neu – sortierbar, mit Filtern, als Baum und mit Details pro Prozess. Er läuft nur, solange er offen ist; mehr unter [Live-Ansicht der Prozesse](help:concepts/live-view).
- Der Reiter **Security** zeigt die Security-Events des Nodes mit Summen, Chart, Top-IPs und Top-Usern; „Im Security-Dashboard öffnen“ führt mit demselben Filter zur Seite [Security](/security) – mehr unter [Security](help:pages/security_events).
- Der Reiter Identität zeigt, ob der Node gesperrt ist, seine letzte Adresse und ein offenes Übernahmefenster; Operatoren rotieren, widerrufen und entsperren dort den Schlüssel oder erlauben die Übernahme nach einer Neuinstallation – mehr unter [Identität des Nodes](help:concepts/node-identity).

## Typische nächste Schritte

1. Auffällige Kennzahl im Chart eingrenzen: Zeitraum wählen, in den Ausschlag hineinzoomen.
2. Über die Hilfe der Metrik typische Ursachen nachlesen.
3. Ein Dateisystem mit naher Prognose aufräumen oder vergrößern, bevor es voll ist.
