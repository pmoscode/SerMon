# 0031 – Logs in Loki: fester Endpunkt, eigener Client, sparsame Labels

- Status: accepted
- Datum: 2026-11-23
- Bezug: KONZEPT.md §3, §4.3, §4.5, §10; ADR 0001, ADR 0024, ADR 0028; IMPLEMENTIERUNGSPLAN M3-06 bis M3-10;
  Threat Model T35, T36, T20

## Kontext

Logs liegen in Loki (D13), mandantenfähig über `X-Scope-OrgID`. Loki hat selbst keine Anmeldung: Wer den Port erreicht
und einen Tenant-Header setzt, liest und schreibt jeden Tenant. Ausgehende Requests laufen sonst über `safehttp`
(ADR 0024), das öffentliche Ziele verlangt und interne nur über die Allowlist des Betreibers zulässt. Loki ist aber
kein Ziel, das ein User wählt, sondern ein fester Teil der Installation im internen Netz.

## Optionen

1. **Loki über `safehttp`** mit der Adresse in der Allowlist: Die Allowlist gälte dann auch für Webhooks der Admins,
   und jeder Admin könnte einen Kanal auf Loki richten und Logs aller Orgs lesen oder fälschen.
2. **Eigener Client nur für die URL des Betreibers** (`SERMON_LOKI_URL`), ohne Proxy und Redirects.
3. **Loki mit vorgeschaltetem Auth-Proxy:** ein weiterer Dienst ohne Gewinn, solange nur der Server Loki erreicht.

## Entscheidung

Gewählt: **Option 2.**

- **Ziel:** genau eine URL aus `SERMON_LOKI_URL` (http oder https, ohne Userinfo, Query und Fragment). Der Client
  folgt keinen Redirects, nutzt keinen Proxy aus der Umgebung und hat feste Zeitlimits und Größengrenzen für
  Antworten. Die Ausnahme von `forbidigo` gilt nur für `adapters/loki`.
- **Netz:** Loki hängt nur im Netz `internal` der Compose-Datei, ohne veröffentlichten Port. Nur der Server erreicht
  ihn.
- **Tenant:** `X-Scope-OrgID` ist immer die Org-UUID aus dem Scope bzw. aus dem Agent-Zertifikat, nie aus Eingaben. Der
  Adapter nimmt eine `tenant.OrgID`, keinen String. Loki läuft mit `auth_enabled: true`, ohne Tenant lehnt er ab.
- **Labels:** nur `node_id`, `source`, `unit`, `severity`.
  - Die Unit kommt vom Host. Der Adapter bereinigt sie (erlaubte Zeichen, höchstens 64 Bytes); M3-07 begrenzt die
    Anzahl pro Node.
  - Alles Weitere (PID, Prozess, UID, Datei, Boot-ID, Transport, `truncated`) geht als structured metadata mit.
- **Format:** Push als JSON mit gzip. Abfragen über `query_range` mit `categorize-labels`, damit Metadaten und Labels
  getrennt zurückkommen.
- **Abfragen:** Den LogQL-Text bauen nur typisierte Bausteine (`domain/logql`) mit Escaping, nie User-Eingaben
  (T20, M3-08).
- **Retention und Grenzen:** Der Compactor löscht nach 14 Tagen (D12) und erlaubt Löschen pro Tenant (M3-10). Dazu
  kommen Grenzen pro Tenant für Ingest, Zeilenlänge und Abfragen. Telemetrie ist aus (T36).
- **Health:** `/readyz` meldet Loki (`/ready`), wenn konfiguriert. Ohne `SERMON_LOKI_URL` nimmt der Server keine
  Logs an, das loggt er beim Start als Warnung.

## Konsequenzen

- Positiv: Die Allowlist der Kanäle bleibt eng. Loki ist nur über den Server erreichbar, und jeder Zugriff trägt den
  Tenant aus dem Scope.
- Negativ: Wer in den Server-Container einbricht, erreicht Loki mit jedem Tenant (wie die Datenbank als `sermon_app`,
  aber ohne RLS als zweite Linie). Abgemildert durch das interne Netz und den gehärteten Container.
- Folgearbeiten: Log-Ingest (M3-07), Abfragen (M3-08), Live-Tail (M3-09), Löschen pro Tenant (M3-10).

## Nachtrag M3-07: Ingest und Ausfall von Loki

Der Agent bestätigt seine Batches über eine gemeinsame Seq (Metriken, Events und Logs in einem WAL). Würde der Server
einen Log-Batch erst nach dem Push bestätigen und Loki fällt aus, stünden auch alle Metriken und Security-Events
dahinter, und damit die Alerts.

- **Gewählt:** Logs nach begrenzter Zeit verwerfen statt Acks pro Inhalt (Protokolländerung, zweite Seq im WAL).
  - Log-Batches warten wie Events im Puffer der Session und gehen beim nächsten Flush nach Loki (höchstens 10 s pro
    Push).
  - Schlägt der Push fehl, wartet die Bestätigung ab diesem Batch. Nach `LogRetryFor` (30 s) werden die Logs
    verworfen und gezählt, und die Bestätigung geht weiter.
  - Danach gilt Loki für `LogDownFor` (30 s) als ausgefallen: Neue Log-Batches werden ohne Versuch verworfen. So hält
    ein langer Ausfall die Metriken je Zeitraum nur einmal kurz an.
  - Was Loki endgültig ablehnt (4xx außer 429), wird sofort verworfen.
- **Grenzen im Server:**
  - Bytes/s pro Node (1 MiB/s, Spitze 8 MiB) über Reconnects und pro Org (3 MiB/s, Spitze 12 MiB, unter Lokis
    Grenze pro Tenant).
  - Zeitfenster 7 Tage bis 5 min voraus.
  - Höchstens 64 Units pro Node, danach `other`.
  - Kein Throttle: Er bremste auch die Metriken.
- **Duplikate:** Ein Batch, den der Server schrieb, aber vor dem Ack verlor, kommt erneut. Loki speichert identische
  Einträge eines Streams nur einmal; das ist gegen das echte Image getestet.
- **Sichtbarkeit:** Zähler `sermon_server_ingest_log_entries_total` und
  `sermon_server_ingest_dropped_log_entries_total{reason}`; das Self-Monitoring meldet `logs_unavailable` an die
  Superadmins (Mail und Band in der UI).

## Nachtrag M3-08: Abfragen und Standards von Loki

- **Abfragen:** `domain/logql` baut Selektor und Zeilenfilter aus typisierten Bausteinen.
  - Werte als Go-Literale (`strconv.Quote`; Loki entquotet mit `strconv`), Units und Text in Regexes mit
    `regexp.QuoteMeta`.
  - Nie ohne Nodes. Die Nodes sind immer die für `view_logs` sichtbaren, im Tenant der Org des Aufrufers.
  - Ein Fuzz-Ziel zerlegt jede Ausgabe mit `strconv.QuotedPrefix` und prüft, dass keine Eingabe die Struktur bricht;
    ein Integrationstest lässt jeden Filter auf dem echten Loki laufen.
- **Gefunden:** Loki 3 hängt ohne Einstellung jedem Stream das Label `service_name` (`unknown_service`) und jedem
  Eintrag das Feld `detected_level` an. Beides ist abgeschaltet (`discover_service_name: []`,
  `discover_log_levels: false`); ein Integrationstest verlangt genau die vier Labels und nur die Felder von SerMon.

## Nachtrag M3-10: Löschen

- `Client.Delete` nutzt die Delete-API des Compactors im Tenant der Organisation. Das Ende wird auf jetzt begrenzt,
  weil Loki Löschungen in der Zukunft ablehnt. Der Start wird auf `DeleteFrom` (2025-01-01) gehoben, vor dem Schema;
  Loki lehnt Starts nahe der Epoche ab.
- `delete_request_cancel_period: 10m` in `deploy/compose/loki.yaml`. `deletion_mode: filter-and-delete` war schon
  gesetzt.
- **Gefunden:** Abfragen sehen eine Löschung erst nach bis zu 5 Minuten; den Cache der Querier stellt Loki nicht
  einstellbar bereit. Tests prüfen deshalb, dass die Anfrage im richtigen Tenant eingetragen ist, und mit einem schnell
  laufenden Compactor (`lokitest.Start` mit Flags), dass Loki sie bearbeitet.
