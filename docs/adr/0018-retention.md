# 0018 – Retention: gestaffelte Auflösung für Metriken, Logs und Events

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert; DB-Auflösung nach Spike M0-09)
- Bezug: KONZEPT.md §2 (D12, D13), §4.5, §8; ADR 0001

## Kontext

Der Agent sammelt jede Sekunde rund 400 Serien. Für die Fehlersuche sind die letzten Minuten in voller Auflösung
wichtig, für Trends und Prognosen Wochen bis ein Jahr in grober Auflösung. Alles in voller Auflösung zu speichern,
sprengt Speicher und Schreiblast (Spike M0-09: rund 320 GB WAL pro Tag bei 1 s für 50 Nodes).

## Optionen

1. **Eine Auflösung, lange Retention:** einfach, aber entweder zu grob für Live-Analysen oder zu teuer.
2. **Gestaffelt mit Downsampling** in der Datenbank (Continuous Aggregates).
3. **Rohdaten extern** (z. B. Objektspeicher), Abfragen bei Bedarf: mehr Komponenten.

## Entscheidung

**D12 – Gestaffelte Retention, konfigurierbar:**

| Daten | Auflösung | Ort | Standard |
|---|---|---|---|
| Metriken | 1 s | Ringpuffer im Server-Speicher | 15 min |
| Metriken | 10 s (avg/min/max) | `metrics_raw` | 24 h |
| Metriken | 1 min | `metrics_1m` | 30 Tage |
| Metriken | 1 h | `metrics_1h` | 1 Jahr |
| Logs | – | Loki | 14 Tage |
| Events, Alerts, Update-Läufe | – | PostgreSQL | 1 Jahr |
| Audit-Log | – | PostgreSQL | 2 Jahre |

Speicher und Schema regelt ADR 0001 (D13).

## Konsequenzen

- Positiv: Live-Charts und kurze Alert-Fenster nutzen 1 s, ohne die Datenbank zu belasten; ein Jahr Trend für < 5 GB.
- Negativ: Nach einem Server-Neustart fehlen die 1-s-Werte der letzten 15 min (nicht persistent); Spitzen unter 10 s
  sind in der Datenbank nur über `max` sichtbar.
- Security/Datenschutz: Logs können personenbezogene Daten enthalten; 14 Tage sind Standard und kürzer einstellbar
  (§18.7).
- Folgearbeiten: Retention-Jobs für Events und Audit-Log (M2/M3); Einstellungen pro Org.

## Nachtrag V1-22 (2026-11-27): Vorgabe der Instanz für Events, kein Bündeln

**Anlass:** Der Lasttest (V1-08) erzeugte mit dem Brute-Force-Szenario (50 Nodes, ein Drittel der Zeit angegriffen)
30 000 Security-Events und 16,6 MiB je Stunde; bei einem Jahr Aufbewahrung rund 140 GiB, mehr als alle Metriken.

**Entscheidung:**

- **`SERMON_EVENT_RETENTION`** (Product Owner): Security-Events und Verlauf der Alerts von Orgs ohne eigene Einstellung
  bleiben ab Werk **90 Tage** statt eines Jahres; erlaubt sind ganze Tage von 7 bis 365 (die Grenzen der Orgs). Orgs
  mit eigener Einstellung behalten sie (7 bis 365 Tage, ADR 0029). Bei 90 Tagen kostet dasselbe Szenario rund 35 GiB.
- **Kein Bündeln** fehlgeschlagener Anmeldungen (Zähler je Quelle und Zeitfenster statt einer Zeile je Versuch).
  Die Regeln zählen gleitend über die genauen Zeitpunkte (`maxInWindow`, `maxNodesInWindow`), `login_after_failures`
  vergleicht jeden Versuch mit der Anmeldung danach, die UI und der CSV-Export zeigen Port und Zeit jedes Versuchs, die
  Übersicht zählt in Schritten bis zu einer Minute. Beim Ingest gebündelt wären die Regeln nicht mehr gleich genau;
  erst nach den Fenstern der Regeln (48 h) gebündelt, bliebe die Engine genau, aber Liste, Export, Übersicht und
  Protokoll bekämen ein zweites Format, ältere Charts würden gröber, und die Ersparnis hinge davon ab, wie viele
  Benutzernamen ein Angreifer durchprobiert. Eine eigene kürzere Frist nur für fehlgeschlagene Anmeldungen hätte die
  Einstellung der Orgs unterlaufen. Die kürzere Vorgabe für alle Events löst das Problem ohne beides.

**Umsetzung:** Der Job der Aufbewahrung (stündlich, `orgretention`) löscht jetzt für **jede** Org: vor ihrer eigenen
Frist oder vor der Vorgabe. Die Datenbank prüft beides gegen ihre eigene Uhr: `security_events_expire_org` nimmt die
Frist der Org, ohne eine die übergebene Vorgabe, nie unter 7 Tagen (die Grenzen von 7 bis 365 Tagen prüft der Server
beim Start). Ganze Monate (`security_events_expire`)
gehen erst, wenn sie vor der Vorgabe **und** vor der längsten Frist aller Orgs enden; ein Sprung der Uhr oder eine
kleine Vorgabe löscht so nichts, was eine Org noch behält (Migration 00083). `alerting.HistoryRetention` (ein Jahr)
bleibt als Grenze für `sermon_app`.

**Konsequenzen:** Bestehende Installationen verlieren beim ersten Lauf nach dem Upgrade Events und Verlauf, die älter
als 90 Tage sind, wenn weder der Betreiber die Vorgabe noch die Org eine eigene Frist gesetzt hat – die Release-Notes
nennen das (V1-15). Wer ein Jahr behalten will, setzt `SERMON_EVENT_RETENTION=8760h` und plant den Platz ein
(Admin-Doku, Betrieb).
