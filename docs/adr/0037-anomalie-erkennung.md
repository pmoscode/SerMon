# 0037 – Anomalie-Erkennung: Baselines, Ergebnisse und Worker

- Status: accepted
- Datum: 2026-11-25
- Bezug: KONZEPT.md §13, §12.3, O8; ADR 0001 (Metrikspeicher), ADR 0018 (Retention); IMPLEMENTIERUNGSPLAN M4-21 bis
  M4-27

## Kontext

KONZEPT §13 legt das Verfahren fest: je (Node, Metrik) eine kurzfristige Baseline (EWMA, Halbwertszeit 1 h) und eine
saisonale (Median und MAD je Stunde der Woche über 4 Wochen), anomal nur, wenn beide über 4 liegen, entprellt mit 3 von
5 Minuten; daraus die Anomalie-Rate je Node, 7 Tage Lernphase. Offen war, wo Baselines und Ergebnisse liegen, wie die
Mandantentrennung ohne RLS auf Hypertables gelingt (O8), was ein Lauf kostet und welche Metriken zählen.

Größenordnung (KONZEPT D6, Budget mit Reserve): 200 Nodes × 30 Metriken = 6 000 Serien, jede Minute ein Wert.
`metrics_1m` hält 30 Tage (ADR 0018), genug für 4 Wochen.

## Optionen

**Saisonale Baseline neu rechnen**

1. **Alle 168 Buckets stündlich:** 6 000 Serien × 40 320 Minuten ≈ 240 Mio. Zeilen je Stunde – zu teuer.
2. **Nur den Bucket der abgelaufenen Stunde:** Neue Daten bekommt jede Stunde nur ihr eigener Bucket der Woche. 6 000 ×
   4 Wochen × 60 Minuten ≈ 1,4 Mio. Punkte je Stunde.
3. **Inkrementelle Quantile (t-digest):** genauer Median ohne Neurechnung, aber Zustand, der Migrationen, zwei
   Instanzen und Retention überleben muss, und ein Verfahren, das sich schlechter erklären lässt.

**Rechnen in SQL oder in Go**

1. **SQL** (`percentile_cont`): wenig Daten über die Leitung, aber die Logik läge zweimal vor (Golden-Tests und
   Benchmark brauchen sie in Go) und in einer `SECURITY DEFINER`-Funktion.
2. **Go** (reines Paket `domain/anomaly`): Die Punkte kommen über die vorhandenen Lesefunktionen (`metric_points_1m`,
   O8); dieselbe Funktion rechnet im Worker, in den Golden-Tests und im Benchmark.

**Kurzfristiger Zustand (EWMA, Entprellung)**

1. **Persistiert** je Serie und Minute: viele Schreibzugriffe, und nach einem Wechsel der Instanz trotzdem veraltet.
2. **Im Speicher des Workers, beim Start aus der Historie aufgewärmt:** 6 h aus `metrics_1m` (sechs Halbwertszeiten,
   Restgewicht 1,6 %) und die letzten 5 Minuten für die Entprellung.

**Ergebnisse je Minute**

1. **Jede Serie jede Minute:** so viele Zeilen wie `metrics_1m` (8,6 Mio. am Tag) – verdoppelt den Speicher.
2. **Nur auffällige Minuten** (Score ab 3 oder anomal): Das Band braucht die anomalen Minuten, „Was war ungewöhnlich?“
   sortiert nach dem höchsten Score; was darunter liegt, zählt als unauffällig. Erwartet sind wenige Prozent.

## Entscheidung

### Metriken

- **Kandidaten** stehen im Katalog (`metric.Definition.Anomaly`): CPU-Modi `user`, `system`, `iowait`, `steal`; Load
  (1 min) je CPU; Speicher verfügbar; Swap-In und -Out; Disk-Lesen, -Schreiben, Auslastung und Wartezeit je Gerät;
  Netz-Empfang und -Senden in Bytes je Interface; TCP-Retransmits, Verbindungen; laufende und blockierte Prozesse,
  Prozesse gesamt; Kontextwechsel; Fork-Rate. Geräte und Interfaces folgen den Ausschlüssen der Standardregeln
  (`loop*`, `ram*`, `lo`, `veth*`, …).
- **Je Node höchstens 64 Serien** (in der Reihenfolge des Katalogs), je Organisation 4 000 und je Instanz 20 000
  (Konfiguration); was darüber liegt, wird nicht bewertet und gezählt (`sermon_anomaly_series_skipped`).
- **Abweichungen von §13:** Die Log-Rate kommt aus dem Zähler `sermon_agent_logs_sent_entries` des Agents (Zuwachs je
  Minute), ohne Aufteilung nach Severity – die kennt nur Loki, und eine Abfrage je Node und Minute wäre zu teuer.
  SSH-Fehlversuche bleiben draußen: Dafür gibt es die Brute-Force-Regeln (§12.3); sie kommen mit der Ausbaustufe über
  die Security-Events.

### Baselines

- Tabelle `anomaly_baselines` (normale Tabelle mit RLS je Organisation): je Serie und Stunde der Woche (0 bis 167, in
  UTC) Median, MAD und Anzahl der Punkte. **Stündlich wird nur der Bucket der abgelaufenen Stunde neu gerechnet**, aus
  den 1-min-Werten derselben Stunde der letzten 4 Wochen (Lesen über `metric_points_1m`). Ein Bucket mit weniger als 30
  Punkten bewertet nicht.
- **Stunde der Woche in UTC:** Die Zeitzone des Nodes kennt der Server nicht; Sommer- und Winterzeit verschieben ein
  lokales Muster zweimal im Jahr um eine Stunde, was das Band eine Woche lang breiter macht.
- **Mindeststreuung:** Ist die MAD 0 (konstante Werte), gilt eine Mindeststreuung abhängig von Einheit und Median
  (Details mit M4-22), damit eine kleine Änderung keine unendliche Abweichung ergibt.

### Ergebnisse

- Hypertable `anomaly_points (ts, series_id, score real, anomalous boolean)` **ohne RLS**, Columnstore nach 2 h,
  Retention 30 Tage wie `metrics_1m`. Geschrieben werden nur Minuten mit Score ≥ 3 oder anomal. `sermon_app` darf
  schreiben, aber nicht lesen; gelesen wird über `SECURITY DEFINER`-Funktionen von `sermon_definer`, die wie
  `metric_points_*` nur Serien der aktuellen Organisation liefern (Join mit der RLS-geschützten `series`, O8).
- **Anomalie-Rate je Node** als Serie `sermon_anomaly_rate_ratio` (Anteil der bewerteten Serien, die anomal sind, in
  ppm) und `sermon_anomaly_series_scored`: im Katalog als „vom Server“ (wie die Update-Serien, M4-16), geschrieben in
  `metrics_raw`. Charts, Heatmap und der Regeltyp `anomaly` lesen sie wie jede Metrik.

### Worker

- Ein Worker je Instanz, jede Minute; je Organisation eine **Lease** in der Datenbank (wie der Orchestrator, M4-13), damit
  zwei Instanzen nicht doppelt rechnen. Organisationen kommen reihum zuerst.
- **Zeitbudget:** ein Lauf höchstens 20 s; was nicht fertig wird, setzt der nächste Lauf fort (Cursor je Organisation),
  Läufe stapeln sich nie. Die stündliche Neurechnung verteilt sich über die Minuten der Stunde.
- **Rechenbudget:** 6 000 Serien je Minute lesen (eine Abfrage je Organisation) und bewerten; Ziel < 2 s und < 50 MiB
  zusätzlich. Die Baselines: 1,4 Mio. Punkte je Stunde, Ziel < 60 s verteilt. Der Benchmark (M4-22) misst die
  Bewertung, der Test mit PostgreSQL (M4-23) die Abfragen.
- **Lernphase:** 7 Tage ab `nodes.learning_since` (seit M3-16, das Enrollment); eine neue Serie ab ihrer ersten Minute
  (`series.created_at`). In der Lernphase rechnet der Worker, schreibt aber keine Anomalie-Rate über 0 – Alerts gibt es
  nicht, die UI zeigt „lernt noch (Tag 3/7)“.

### Schnittstelle

- `domain/anomaly` ist rein (ohne I/O): `Baseline` aus Punkten, `Detector` mit `Score(state, at, value, baseline)`
  für die Ausbaustufe (Isolation Forest, k-means), `Debouncer` für 3 aus 5.
- RPCs (M4-23): Anomalien eines Nodes im Zeitraum (Band, je Serie die anomalen Minuten), „Was war ungewöhnlich?“ für
  einen Node oder die Fleet im Zeitraum (höchstens 50 Serien nach Score), beide im Scope des Aufrufers und mit Grenzen
  wie `QueryMetrics`.

## Folgen

- Zwei neue Speicherobjekte (`anomaly_baselines`, `anomaly_points`) mit eigenen Lesefunktionen; die Tenant-Tests decken
  sie ab wie die Metriken.
- Nach einem Wechsel der Instanz braucht der Worker einen Lauf, um sich aufzuwärmen; bis dahin bewertet er nicht.
- Muster im Rhythmus der lokalen Zeit verschieben sich mit der Sommerzeit (UTC-Buckets).
- Severity der Logs und SSH-Fehlversuche fehlen gegenüber §13 bis zur Ausbaustufe.
