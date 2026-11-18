# 0001 – Zeitreihen-Speicher: TimescaleDB statt VictoriaMetrics

- Status: accepted (vorbehaltlich Spike in M0)
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
   - `series(id int, org_id uuid, node_id uuid, metric text, labels jsonb)`, eindeutig über `(org_id, node_id, metric, labels)`
   - `metrics_raw(ts timestamptz, series_id int, value double precision)`
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
  - Spike in M0 durchführen und die Ergebnisse in diesem ADR nachtragen.
  - Schema und Migrationen (`goose`) anlegen.
  - `MetricStore`-Port mit Contract-Tests schreiben. Dieselben Tests müssen später auch eine alternative Implementierung bestehen.
