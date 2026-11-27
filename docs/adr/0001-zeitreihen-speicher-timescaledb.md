# 0001 – Zeitreihen-Speicher: TimescaleDB statt VictoriaMetrics

- Status: accepted (Spike M0-09 durchgeführt, siehe „Ergebnis des Spikes“)
- Datum: 2026-11-18
- Bezug: KONZEPT.md §2 (D6, D12, D13), §4.3, §4.5, §8, §22, §24 (O1, O2, O8, O12)

## Kontext

SerMon speichert Metriken von bis zu ca. 50 Nodes mit einer Auflösung von 1 s.
Etwa 400 Serien pro Node ergeben 20 000 Samples/s, also ca. 1,7 Mrd. Samples pro Tag.

Gefordert ist eine **gestaffelte Aufbewahrung (D12):**

| Auflösung | Aufbewahrung |
|---|---|
| 1 s | 24 h |
| 1 min | 30 Tage |
| 1 h | 1 Jahr |

Weitere Randbedingungen:

- **Self-hosted per docker-compose (D23).** Betrieb und Backup sollen einfach bleiben.
- **Mandantenfähig (D5).** Die Isolation zwischen Orgs ist sicherheitskritisch.
- **Security hat oberste Priorität.** Wenige Komponenten bedeuten eine kleinere Angriffsfläche.
- PostgreSQL ist für Stammdaten, Events und Audit-Log ohnehin gesetzt.

Ursprünglich war in D13 „PostgreSQL + VictoriaMetrics + Loki“ gewählt.
Bei der Ausarbeitung zeigte sich ein Problem: **Downsampling ist bei VictoriaMetrics ein Enterprise-Feature.**
Die gestaffelte Aufbewahrung wäre in der Open-Source-Version nur mit einem Umweg möglich:
vmagent-Stream-Aggregation und **drei** VictoriaMetrics-Instanzen (1 s / 1 min / 1 h).
Der Server müsste dann je nach Zeitraum die passende Instanz wählen.

## Optionen

### 1. PostgreSQL + VictoriaMetrics (Open Source) + vmagent-Aggregation

- **Vorteile:**
  - Sehr effizient: ca. 0,5–1 Byte/Sample, hoher Ingest-Durchsatz.
  - MetricsQL und Prometheus-kompatibel, ideal für das Prometheus-Scraper-Modul.
  - Skaliert weit über 1 000 Nodes.
  - Apache-2.0.
- **Nachteile:**
  - Fünf Komponenten (PG, vmagent, 3× VM) nur für Stamm- und Metrikdaten.
  - Backups über mehrere Systeme hinweg müssen konsistent sein.
  - VM-single kennt keine Mandanten. Die Isolation hängt allein an einem `org_id`-Label, das der Server einfügt.
  - Keine JOINs mit Stammdaten (Gruppen, Tags, Rechte) möglich. Erst kommt eine PG-Abfrage, dann eine Label-Liste an VM.
  - Die Abfragelogik muss je nach Zeitraum zwischen drei Instanzen umschalten.

### 2. PostgreSQL + TimescaleDB (eine Instanz)

- **Vorteile:**
  - Gestaffelte Aufbewahrung **nativ**: hierarchische Continuous Aggregates und eine Retention-Policy pro Stufe.
  - **Eine** Datenbank für Stammdaten, Events und Metriken. Ein Backup-Weg (pg_dump bzw. pgBackRest).
  - Mandantentrennung mit denselben Mitteln wie im restlichen Datenmodell (Scope-Pflicht, ggf. RLS).
  - Direkte JOINs auf Gruppen, Tags und Rechte.
  - SQL ist im Team bekannt; Tests laufen mit Testcontainers gegen genau eine Abhängigkeit.
- **Nachteile:**
  - Höherer Speicherbedarf: komprimiert ca. 2–4 Byte/Sample, unkomprimierte Chunks deutlich mehr.
  - Hohes WAL-Volumen und viel I/O bei 1-s-Auflösung.
  - Keine PromQL/MetricsQL. Raten und Aggregationen laufen in SQL; in Continuous Aggregates sind keine Window-Funktionen möglich.
  - Kompression und Continuous Aggregates stehen unter der **Timescale License (TSL)**, nicht unter Apache-2.0.
  - Grenze erfahrungsgemäß bei einigen hundert Nodes mit 1-s-Auflösung.

### 3. Alles eingebettet (SQLite + eigene TSDB im Server)

- **Vorteile:**
  - Keine externen Abhängigkeiten.
- **Nachteile:**
  - Eigene TSDB bedeutet hohen Entwicklungs- und Testaufwand.
  - Schlechtere Skalierung, kein bewährtes Backup- und Werkzeug-Ökosystem.

## Entscheidung

Gewählt wird **Option 2: PostgreSQL 17 + TimescaleDB** für Stammdaten, Events und Metriken.
**Loki bleibt für Logs**, weil Volltextsuche über mehrere GB pro Tag nicht die Stärke von PostgreSQL ist und Loki Mandantentrennung mitbringt.

Begründung: Für die Zielgröße (< 50 Nodes) überwiegen die einfache Betriebsführung, die native gestaffelte Aufbewahrung und die einheitliche Mandantentrennung.
Die bessere Effizienz von VictoriaMetrics braucht SerMon in dieser Größenordnung nicht.

### Umsetzungsvorgaben

1. **Port `MetricStore`** in `server/internal/app`, implementiert in `server/internal/adapters/timescale`.
   Keine andere Schicht greift direkt auf Metrik-Tabellen zu. Dadurch bleibt ein späterer Wechsel des Speichers möglich.
2. **Schmales Schema:**
   - `series(id bigint, org_id uuid, node_id uuid, metric text, labels jsonb, seen_at timestamptz)`, eindeutig über
     `(org_id, node_id, metric, labels)`
   - `metrics_raw(ts timestamptz, series_id bigint, value double precision)`
3. **Hypertable** `metrics_raw` mit 1-h-Chunks. Kompression nach 2 h (`segmentby = series_id`, `orderby = ts`). Aufbewahrung 24 h.
4. **Continuous Aggregates:**
   - `metrics_1m` (Basis `metrics_raw`, 30 Tage)
   - `metrics_1h` (hierarchisch auf `metrics_1m`, 1 Jahr)
   - Je Bucket `avg`, `min`, `max`, `last`
   - Refresh-Policies mit kleinem Offset
5. **Counter in Raten umrechnen:** Der Agent rechnet Counter beim Messen in Raten pro Sekunde um (Reset- und Wrap-Erkennung) und sendet sie als Gauges.
   So kommen die Aggregate ohne Window-Funktionen aus, und Werte aus dem WAL-Puffer bleiben korrekt.
6. **Ingest:** Batches aller Agents werden gesammelt und einmal pro Sekunde per `COPY` (pgx) geschrieben.
   Die Zuordnung von Serien zu IDs wird im Speicher gecacht. Es gelten Cardinality-Limits pro Node.
7. **Ringpuffer:** Der Server hält die letzten 15 min mit 1-s-Auflösung im Speicher, für Live-Charts und kurze Alert-Fenster.
8. **Mandantentrennung:**
   - Jede Methode des `MetricStore` erwartet einen `tenant.Scope` und filtert über `series.org_id` und die erlaubten `node_id`s.
   - Tenant-Isolation-Tests sind Pflicht.
   - RLS kommt nur dort zum Einsatz, wo TimescaleDB es auf Hypertables und Aggregaten unterstützt (siehe Spike).
9. **Image:** `timescale/timescaledb` (pg17) in docker-compose.

### Spike in M0 (Abnahmekriterien)

Mit `sermon-agent --simulate` und 50 Nodes × 400 Serien × 1 s auf 4 vCPU, 8 GB RAM und NVMe:

- [ ] Ingest-Lag p99 < 2 s über 24 h Dauerlast
- [ ] DB-CPU im Mittel < 50 %
- [ ] Plattenbedarf nach 24 h ≤ 25 GB; WAL-Volumen gemessen und dokumentiert
- [ ] Abfrage-Latenz p95: 1-h-Chart (1 s) < 200 ms, 24-h-Chart < 300 ms, 30-Tage-Chart (1 min) < 300 ms
- [ ] Continuous Aggregates laufen ohne Rückstand
- [ ] Verhalten von RLS auf Hypertables, komprimierten Chunks und Continuous Aggregates geklärt

**Fallback, wenn die Kriterien verfehlt werden:** 1-s-Werte nur im Ringpuffer halten und erst ab 5–10 s in der DB speichern.
Das reduziert die Last um den Faktor 5–10. Erst danach wäre ein Wechsel auf Option 1 neu zu bewerten.

### Ergebnis des Spikes (M0-09, 2026-11-19)

**Aufbau:**
- TimescaleDB 2.32 (pg17) in Docker mit **4 CPUs und 6 GB RAM**; 8 GB waren in der Docker-VM nicht möglich.
- Apple M3 Max mit interner SSD.
- Messprogramm: `spikes/timescale/`, Rohdaten in `spikes/timescale/results/`.
- 50 Nodes × 400 Serien × 1 s = **20 000 Messwerte/s**, ein `COPY` pro Sekunde.
- Parallel 5 Dashboards, die alle 2 s einen 1-h-Chart abfragen.
- **Abweichung:** Dauerlast 20 min statt 24 h; die Tageswerte sind hochgerechnet.

| Kriterium | Ergebnis |
|---|---|
| Ingest-Lag p99 < 2 s | ✅ 60 ms ohne Index, **116–154 ms** mit Index (max. 393 ms); 0 Schreibfehler |
| DB-CPU im Mittel < 50 % | ✅ **2,5–3 %** mit Index. Ohne Index 40 %: Die Chart-Abfragen durchsuchten alle Serien |
| Plattenbedarf 24 h ≤ 25 GB | ⚠️ bei 1 s: ~17 GB komprimiert (Gleitkomma, 10 B/Wert) + ~9 GB unkomprimiertes 2-h-Fenster ≈ 26 GB. Mit **ganzzahligen Werten 3,1 B/Wert** ≈ 14 GB ✅ |
| WAL-Volumen | ⚠️ **13–15 GB/h ≈ 320 GB/Tag** bei 1 s mit Index (ohne Index 6,9 GB/h). `wal_compression = lz4` und seltenere Checkpoints bringen nur ~11 % |
| 1-h-Chart < 200 ms | ❌ ohne Index 720 ms (p95) → ✅ mit Index `(series_id, ts DESC)` **16–21 ms** unter Last, 120 ms auf historischen Daten (erster, kalter Lauf 401 ms) |
| 24-h-Chart < 300 ms | ✅ aus `metrics_1m` **11 ms**, aus Rohdaten 157 ms (p95) |
| 30-Tage-Chart < 300 ms | ✅ aus `metrics_1m` **77 ms**, aus `metrics_1h` 6 ms (p95) |
| Continuous Aggregates ohne Rückstand | ✅ alle Refresh- und Kompressions-Jobs erfolgreich, Watermark von `metrics_1m` aktuell |
| RLS auf TimescaleDB-Objekten | ❌ **nicht unterstützt** auf Hypertables mit Columnstore und auf Continuous Aggregates bzw. deren Materialisierung. Eine Rolle ohne Sonderrechte sieht ohne Mandant alle Messwerte |
| Kompression | Faktor 6 (Random-Walk-Gleitkomma) bis 10,8 (glatte Verläufe); Backfill mit ~335 000 Zeilen/s |

**Entscheidungen aus dem Spike:**

1. **Auflösung in der DB: 10 s** (Entscheidung des Product Owners, 2026-11-19). Der Agent misst weiter jede
   Sekunde; der Server hält die letzten **15 min mit 1 s im Ringpuffer** (Live-Ansicht, Live-Charts, kurze
   Alert-Fenster).
   - `metrics_raw` speichert je Serie und 10-s-Fenster `avg`, `min` und `max`.
   - Erwartete Werte: ca. 32–40 GB WAL/Tag und < 5 GB Platz für 24 h.
   - Die Auflösung ist konfigurierbar (1, 5 oder 10 s).
2. **Index `(series_id, ts DESC)`** auf `metrics_raw` ist Pflicht.
3. **Werte ganzzahlig** in Basiseinheiten (Bytes/s, Pakete/s, ppm), weil das die Kompression etwa verdreifacht (KONZEPT §8).
4. **Zweitlinie für die Mandantentrennung ohne RLS:**
   - `sermon_app` erhält **kein direktes SELECT** auf `metrics_raw`, `metrics_1m` und `metrics_1h`.
   - Gelesen wird über eine `SECURITY DEFINER`-Funktion, deren Eigentümer eine eigene Rolle ohne Sonderrechte ist.
     Für diese Rolle gilt die RLS auf `series` (dort geprüft: ohne Mandant 0 Zeilen). Die Funktion liefert deshalb nur
     Messwerte von Serien des aktuellen Mandanten.
   - Schreibrechte (INSERT) bleiben für den Ingest. Die Identität stammt dort aus dem Zertifikat.
   - Umsetzung und Tests folgen in M1-22.
5. **PostgreSQL-Einstellungen:** `wal_compression = lz4`, `max_wal_size = 8GB`, `checkpoint_timeout = 15min`.
   Die von `timescaledb-tune` gesetzten 1 GB `max_wal_size` sind für Dauer-Schreiblast zu klein.

## Konsequenzen

- **Positiv:**
  - Statt fünf Speicherkomponenten nur noch eine (+ Loki).
  - Ein Backup- und Restore-Pfad.
  - Einfachere lokale Entwicklungsumgebung und Integrationstests.
  - JOINs mit Stammdaten für Fleet-Ansichten, Heatmaps und rechtebasierte Filter.
  - Die gestaffelte Aufbewahrung ohne Umwege und ohne Enterprise-Lizenz.
- **Negativ / Risiken:**
  - **Last bei 1-s-Auflösung (O1).** Gegenmaßnahmen: Spike, `COPY`-Batching, Kompression, Ringpuffer, Fallback auf 5–10 s.
  - **Höherer Plattenbedarf.** Eine NVMe-SSD ist Pflicht (§22).
  - **Keine PromQL.** Das Prometheus-Scraper-Modul speichert in das generische Serien-Schema; Histogramme werden in v1 nur als Buckets bzw. Summen abgebildet.
  - **Lizenz (O2).** Die TSL erlaubt Self-Hosting und den Einsatz als Teil eines Produkts bzw. SaaS. Verboten ist nur, TimescaleDB selbst als Datenbank-Dienst anzubieten. Vor einem SaaS-Start juristisch prüfen.
  - **Grenze bei ca. 200 Nodes (O12).** Dann wird die Implementierung des `MetricStore` auf VictoriaMetrics umgestellt. Die Domänenlogik ist nicht betroffen.
- **Security-Auswirkungen:**
  - Kleinere Angriffsfläche, weil weniger Dienste und Netzwerkpfade existieren.
  - Mandantentrennung mit einheitlichen Mitteln statt reiner Label-Konvention.
  - Abfragen werden ausschließlich typisiert gebaut (sqlc / parametrisiert). User schreiben kein SQL.
- **Folgearbeiten:**
  - ✓ Spike in M0 durchgeführt (siehe oben).
  - ✓ Schema und Migration `00013_metrics.sql`, `MetricStore`-Port mit Contract-Tests (`metricstoretest`), bestanden von
    der TimescaleDB-Implementierung (`adapters/timescale`) und einem Speicher-Fake (M1-22).

## Nachtrag M1-36 (Security-Review M1-35)

- **Serien-IDs sind `bigint`.** IDs werden nie wiederverwendet; mit `integer` wäre nach 2³¹ Registrierungen der Ingest
  aller Mandanten stehen geblieben. Den Typ einer Spalte ändert TimescaleDB nicht mehr, sobald Chunks komprimiert
  sind (ausprobiert: „operation not supported on hypertables with compressed chunks“); eine Migration mit Daten müsste
  alles entpacken und beide Continuous Aggregates neu aufbauen, mit Verlust der Historie jenseits der 24 h Rohdaten.
  Deshalb wurde `00013_metrics.sql` vor dem ersten Release direkt geändert. Entwicklungsdatenbanken müssen einmal neu
  angelegt werden (`docker compose -f deploy/dev/compose.yaml down -v`); Migration 00017 bricht auf einer älteren
  Datenbank mit genau diesem Hinweis ab, statt Charts mit „function does not exist“ zu hinterlassen.
- **`series.seen_at`** (Migration 00017) hält die Zeit der neuesten Daten einer Serie, vom Store höchstens alle
  6 Stunden fortgeschrieben (`metrics.SeenResolution`, eine Aktualisierung pro Schreibvorgang mit dessen neuester
  Datenzeit). `sermon_app` darf nur diese Spalte ändern.
- **`MetricStore` kennt `NodeSeries` und `OrgSeriesCount`** (aktive Serien eines Nodes bzw. einer Org seit einem
  Zeitpunkt), mit Contract-Tests für beide Implementierungen. Der Ingest zählt damit das Serien-Limit pro Node
  (2 000) über Sessions und Instanzen hinweg und begrenzt die Serien einer Org (250 000); als aktiv gilt eine Serie mit
  Daten in den letzten 48 Stunden, eine eingestellte (entfernte Platte) gibt ihren Platz danach frei.
- **Speicher:** Der Ringpuffer hält höchstens 2 000 Serien pro Node, der Serien-Cache des Adapters höchstens 500 000
  Einträge (danach beginnt er neu, er spart nur Abfragen).

## Nachtrag V1-21: Kompression der Continuous Aggregates

- **Anlass:** Im Lasttest (V1-08) waren `metrics_1m` und `metrics_1h` unkomprimiert, hochgerechnet 30,6 und 5,9 GiB
  bei voller Aufbewahrung – der größte Teil der Metriken.
- **Entscheidung (Migration 00082):** Beide bekommen den Columnstore wie die Rohdaten, `segmentby = series_id`,
  `orderby = bucket`: Die Leser (`metric_points_1m`, `metric_points_1h`) filtern nach Serien und Zeitraum, ein Chart
  entpackt nur die Segmente seiner Serien (Index auf `series_id` und den Grenzen des Segments). `metrics_1m` wird nach
  **6 h** komprimiert (Chunks 10 h, unverändert), `metrics_1h` nach **einem Tag**; `metrics_1h` bekommt Chunks von
  **7 Tagen** statt 10 h, sonst hielte ein Segment nur zehn Zeilen einer Serie. Die Fenster der Refresh-Policies
  (15 min, 3 h) liegen immer in unkomprimierten Chunks; Nachzügler und das Löschen von Nodes und Orgs schreiben bzw.
  löschen auch in komprimierten Chunks (TimescaleDB 2.32, getestet).
- **Gemessen:** Lasttest 2 h mit 50 Nodes, `metrics_1m` danach von Hand komprimiert (`COMPRESS_AGGREGATES=1`): auf
  15,9 % – mit zwei Stunden je Segment statt zehn, also eher zu schlecht. Hochgerechnet `metrics_1m` 5,2 GiB nach
  30 Tagen und `metrics_1h` 1,0 GiB nach einem Jahr (im Verhältnis von `metrics_1m`) statt 30,6 und 5,9 GiB. Lesen
  aus komprimierten Chunks: in einer Probe mit 3 000 Serien 4 ms für 4 Serien über zwei Tage wie unkomprimiert, 0,2 s
  statt 0,5 s für 300 Serien; Charts im Lasttest p95 9 bis 16 ms.
- **Grenze:** Die Chunk-Größe von `metrics_1h` gilt nur für neue Chunks; bestehende Installationen komprimieren ihre
  alten 10-h-Chunks schlechter, bis die Aufbewahrung sie entfernt.
