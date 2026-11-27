# 0044 – Zählungen der Log-Zeilen für das Histogramm

- Status: accepted
- Datum: 2026-11-27
- Bezug: KONZEPT.md §4.5, §10.4, D12, D13; ADR 0001, ADR 0031, ADR 0042; IMPLEMENTIERUNGSPLAN V1-23, V1-24;
  Threat Model T131

## Kontext

Das Histogramm des Log-Explorers zählt die Zeilen des Zeitraums je Schweregrad. Bisher fragte der Server dafür Loki
(`sum by (severity) (count_over_time(…[step]))`); Loki zählt dabei bei jeder Abfrage jede Zeile des Zeitraums neu.
Gemessen in V1-23: rund 0,7 s je Stunde Logs bei 50 Nodes und 143 Zeilen pro Sekunde, über 14 Tage hochgerechnet
rund 220 s – weit über der Zeitgrenze von Loki (1 min) und der des Clients (30 s). Die UI bietet 14 Tage an.

Randbedingungen: dieselben Zahlen wie Loki für denselben Filter, Mandantentrennung wie bisher, keine Abfragen von
Usern, Löschen mit Nodes, Orgs und der Aufbewahrung, wenig Betrieb.

## Optionen

1. **Zählen beim Ingest** je Node, Quelle, Schweregrad und Minute in TimescaleDB (wie eine Metrik); das Histogramm
   liest die Zählungen, wo der Filter es erlaubt.
   - Pro: Kosten unabhängig von der Zahl der Zeilen; nach jedem Neustart sofort schnell; Nodes, Gruppen, Quellen und
     Schweregrade filtern wie bisher.
   - Contra: eine zweite Ablage, die mit Loki übereinstimmen muss (Duplikate, Ablehnungen); Text und Units kann sie
     nicht.
2. **Lokis Ergebnis-Cache** (`results_cache`, an den Schritt ausgerichtete Zeiträume, Aufteilung nach Intervall).
   - Pro: keine zweite Ablage.
   - Contra: Die erste Abfrage eines Filters zählt weiter alle Zeilen (220 s, scheitert); der Cache hängt am
     LogQL-Text, und der nennt jeden Node: jede andere Auswahl und jeder neue Node ist ein Fehlschlag. Im Single-Binary
     ein Cache im Speicher (Neustart leert ihn), mehr Speicher für Loki, das schon nahe an seinem `GOMEMLIMIT` liegt.
3. **Aggregierte Metriken von Loki** (Pattern-Ingester, `__aggregated_metric__`): experimentell, an `service_name`
   gebunden (bei SerMon abgeschaltet, ADR 0031), weiterer Speicher in Loki.

## Entscheidung

Gewählt: **Option 1**, weil nur sie das kalte Histogramm über 14 Tage unter der Zeitgrenze hält.

- **Ablage:** Hypertables `log_counts_1m` und `log_counts_1h` (Migration 00084): Organisation, Node, Quelle,
  Schweregrad, Bucket, Zeilen. Ein Bucket `k` zählt die Zeilen mit `k − 1 min < at ≤ k` (bzw. eine Stunde) – genau so
  zählt Lokis `count_over_time` einen Schritt (gegen das echte Image geprüft). Columnstore nach einem Tag
  (segmentiert nach Node), Aufbewahrung 14 Tage wie Loki (ganze Chunks, also bis zu einem Tag länger).
- **Schreiben:** Der Ingest zählt nach jedem erfolgreichen Push zu Loki die Zeilen dieses Pushs je Batch, Minute,
  Quelle und Schweregrad (Units und Text nicht) und schreibt sie über `log_counts_add`. Was Loki ablehnt oder was
  verworfen wird, zählt nicht. Ein Batch, dessen Bestätigung verloren ging, kommt erneut; Loki speichert ihn einmal,
  `log_count_marks` (höchste gezählte Seq je Node, mit RLS) zählt ihn einmal. Scheitert das Schreiben der Zählungen,
  wartet die Bestätigung wie bei Loki höchstens `LogRetryFor` (30 s) – die Zeilen stehen schon in Loki, sie gehen
  nicht noch einmal hin –, danach werden die Zählungen verworfen und gezählt
  (`sermon_server_ingest_uncounted_log_entries_total`).
- **Lesen:** Schritte von etwa einem Hundertstel des Zeitraums, ab 1 min in ganzen Minuten, ab 30 min in ganzen
  Stunden, an Vielfachen des Schritts seit der Epoche. Ohne Text und Units und mit einem Schritt ab 1 min (Zeiträume ab
  100 min) antworten die Zählungen (Stunden-Tabelle für ganze Stunden, sonst die Minuten), sonst Loki mit denselben
  Punkten. Braucht Loki dafür zu lange (Zeitgrenze des Clients oder 504), ist das `FAILED_PRECONDITION` mit der Bitte,
  den Zeitraum zu verkürzen; die UI sagt das am Histogramm.
- **Mandanten:** Der Columnstore kennt keine RLS (wie die Metriken, ADR 0001, O8). `sermon_app` hat auf den beiden
  Tabellen kein Recht; schreiben, lesen und löschen nur die `SECURITY DEFINER`-Funktionen `log_counts_add`,
  `log_counts_range` und `log_counts_expire`, jede nur in `current_org_id()` und für Nodes dieser Organisation
  (`sermon_definer` liest alle Nodes, die Funktion prüft die Organisation selbst). Die Nodes einer Abfrage sind wie
  bisher die für `view_logs` sichtbaren.
- **Löschen:** Fremdschlüssel auf `nodes` mit `ON DELETE CASCADE`: `node_purge` und `org_purge` nehmen die Zählungen
  mit (auch aus komprimierten Chunks, getestet). Die Aufbewahrung je Org (M5-30) löscht sie im selben Lauf und bis zur
  selben Grenze wie die Logs in Loki.
- **Backup:** wie die Logs in Loki nicht im logischen Backup (`postgres.BackupExclusions`, ADR 0042).

## Konsequenzen

- Positiv: Das Histogramm über 14 Tage ohne Text und Units liest einige hunderttausend Zeilen der Datenbank statt
  hunderte Millionen Log-Zeilen; Loki ist dafür nicht mehr nötig. Gemessen in V1-24 (IMPLEMENTIERUNGSPLAN).
- Negativ: eine zweite Ablage. Wo sie von Loki abweichen kann: Lehnt Loki einen Push ganz ab (4xx), kann er einen Teil
  doch gespeichert haben – gezählt wird nichts; stürzt der Server zwischen Push und Zählung ab, fehlt die Zählung (der
  Agent sendet den Batch erneut, Loki verwirft das Duplikat, die Zählung folgt dann). Text- und Unit-Filter über lange
  Zeiträume bleiben langsam und enden mit der Meldung. Zeilen, die Loki wegen ihrer Aufbewahrung schon gelöscht hat,
  zählen bis zu einem Tag länger (ganze Chunks). Gezählt wird ab der Migration: Nach dem Update zeigt das Histogramm
  über die Tage davor bis zu 14 Tage lang weniger Zeilen, als Loki hat (Release-Notes, V1-15); ein Nachzählen aus
  Loki hätte jede Zeile der 14 Tage einmal gelesen – genau das, was diese Entscheidung vermeidet.
- Gefunden: Loki 3.7 lässt bei einer Metrik-Abfrage manchmal Zeilen des ersten ausgewerteten Punkts weg (je nach
  Filter verschieden). Der Server fragt Loki deshalb ab einem Punkt vor dem Zeitraum und verwirft diesen; ein
  Integrationstest vergleicht beide Quellen für dieselben Zeilen, auch auf den Grenzen von Minuten und Stunden.
- Security-Auswirkungen: zwei Tabellen ohne RLS mit Mandantenbezug (T131); geschützt wie die Metriken durch fehlende
  Rechte von `sermon_app` und die Prüfung der Organisation in den Definer-Funktionen; die Zählungen enthalten keine
  Inhalte der Zeilen, keine Units und keine Texte.
- Folgearbeiten: keine.
