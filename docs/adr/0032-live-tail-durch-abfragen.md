# 0032 – Live-Tail durch wiederholte Abfragen statt Loki-WebSocket

- Status: accepted
- Datum: 2026-11-23
- Bezug: KONZEPT.md §10.4; ADR 0011, ADR 0031; IMPLEMENTIERUNGSPLAN M3-09; Threat Model T26, T35

## Kontext

Der Log-Explorer zeigt neue Zeilen live (KONZEPT §10.4). Loki bietet dafür `/loki/api/v1/tail` als WebSocket. Der
Server spricht mit Loki bisher nur HTTP mit festen Zeitlimits (ADR 0031). Die Zeilen kommen nicht in der Reihenfolge
ihrer Zeitstempel an: Jeder Agent schickt seine Einträge gebündelt (etwa jede Sekunde), nach einem Ausfall aus dem
Outbox auch Minuten später, und jeder Host hat seine eigene Uhr.

## Optionen

1. **Loki-WebSocket je Stream:** eine neue Abhängigkeit (WebSocket-Client), eine offene Verbindung zu Loki pro
   Browser-Tab und eigene Grenzen in Loki (`max_concurrent_tail_requests`). Der Query-Frontend-Pfad wäre ein anderer
   als bei den Abfragen.
2. **Wiederholte Abfragen** über `query_range` mit demselben Client und denselben Filtern wie M3-08: ein Fenster der
   letzten Sekunden, alle 2 s, Duplikate werden im Server entfernt.
3. **Abzweig im Ingest:** Der Server reicht neue Zeilen direkt an offene Streams weiter. Bei mehreren Instanzen landet
   ein Agent aber nicht auf der Instanz des Browsers; das bräuchte einen Bus.

## Entscheidung

Gewählt: **Option 2.**

- **Runde:** Alle 2 s fragt der Stream das Fenster `[max(Start, jetzt − 30 s), jetzt + 1 s)` ab, die neuesten Zeilen
  zuerst. Die 30 s decken die Verzögerung durch das Bündeln im Agent und den Weg über Server und Loki.
- **Duplikate:** Der Stream merkt sich die gesendeten Zeilen des Fensters (Stream-Labels, Zeitstempel, Text) und
  sendet jede genau einmal. Was aus dem Fenster fällt, wird vergessen.
- **Reihenfolge:** Eine Nachricht hält ihre Zeilen nach Zeit aufsteigend. Eine verspätete Zeile kann älter sein als
  eine schon gesendete; die Oberfläche sortiert ein. Zeilen, die mehr als 30 s zu spät ankommen (Outbox nach einem
  Ausfall), zeigt der Tail nicht, die Abfrage aus M3-08 schon.
- **Start:** jetzt oder ein Zeitpunkt der letzten 5 Minuten (`since`), etwa die neueste Zeile einer vorherigen
  Abfrage, damit nichts fehlt.
- **Drosselung:** höchstens 100 Zeilen/s pro Stream, also 200 je Runde. Hält das Fenster mehr neue Zeilen, gehen die
  neuesten raus, der Rest gilt als übersprungen (eine Untergrenze verhindert, dass er später nachkommt), und die
  Nachricht trägt `throttled`.
- **Grenzen wie T26:** Tail und `WatchMetrics` teilen sich die Zähler: 32 Streams pro Session, 64 pro User, 250 pro
  Org, 1 000 pro Instanz. Ein Stream läuft höchstens 15 Minuten, dann öffnet der Client einen neuen.
- **Rechte:** Alle 30 s prüft der Stream die Session erneut (Abmeldung beendet ihn mit `UNAUTHENTICATED`, geänderte
  Rechte beenden ihn normal) und bestimmt die sichtbaren Nodes neu (Node verschoben, Viewer-Schalter). Fehlt
  `view_logs` danach, endet er mit `PERMISSION_DENIED`.
- **Loki nicht erreichbar:** Der Stream bleibt offen, meldet `unavailable` und fragt weiter; die nächste Nachricht nach
  der Erholung trägt es nicht mehr.
- **Lebenszeichen:** Ohne neue Zeilen geht alle 15 s eine leere Nachricht raus, damit Proxys die Verbindung nicht
  schließen.

## Konsequenzen

- Keine neue Abhängigkeit, und mehrere Server-Instanzen bleiben ohne Bus möglich: Jede fragt Loki selbst.
- Last auf Loki: höchstens 0,5 Abfragen/s pro Stream über ein Fenster von 30 s mit Node-Selektor, bei 1 000 Streams
  500 Abfragen/s. Der Ingest begrenzt das Volumen pro Node (M3-07), die Runde liest höchstens 201 Zeilen.
- Verzögerung bis zur Anzeige: Bündeln im Agent (bis 1 s) plus bis zu 2 s bis zur nächsten Runde.
- Sehr spät ankommende Zeilen fehlen im Tail; das ist dokumentiert und die Abfrage zeigt sie.
