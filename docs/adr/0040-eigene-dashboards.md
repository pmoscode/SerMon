# 0040 – Eigene Dashboards: Modell, Rechte, Grid-Editor und Kiosk-Token

- Status: accepted
- Datum: 2026-11-26
- Bezug: KONZEPT.md §7 (Rollen), §16 (Ausbau), §17 (Zeit, Performance, Barrierefreiheit), §18.3 (CSP), D3, D11,
  D27; ADR 0003 (CSP), ADR 0007 (RLS), ADR 0009 (Token als Hash); IMPLEMENTIERUNGSPLAN M5-22 bis M5-27

## Kontext

KONZEPT §16 sieht eigene Dashboards vor: ein Grid mit Drag & Drop, Widgets (Zeitreihe, Gauge, Stat, Tabelle,
Log-Stream, Alert-Liste), Variablen (Node, Gruppe), Teilen in der Org, Export und Import als JSON; dazu einen Kiosk-Modus
mit einem schreibgeschützten, widerrufbaren Token, der an Dashboard und Org gebunden ist.

Offen waren:

- **Das Modell:** Was ein Dashboard enthält, wie es versioniert ist und welche Grenzen gelten. Ein Dashboard ist eine
  gespeicherte Abfrage-Sammlung: Ohne Grenzen wäre es ein Hebel, mit einem Klick (oder einem Fernseher im Flur) die
  Datenbank zu belasten.
- **Rechte:** Wer ein Dashboard sieht und ändert. Die Rollen gelten pro Node-Gruppe; ein geteiltes Dashboard zeigt aber
  jedem Mitglied dieselben Widgets.
- **Abfragen:** Ein eigener Abfragepfad je Widget wäre eine zweite Stelle, an der Scope, Grenzen und Rate-Limits stimmen
  müssen.
- **Grid-Editor:** Bundle-Budget (< 200 KiB gzip pro Seite, Flotte und Node-Seite mit 10 KiB Luft), CSP ohne
  `'unsafe-inline'` (dynamische `style`-Attribute sind blockiert, ADR 0003), Bedienung per Tastatur (WCAG 2.2 AA),
  Lizenz kompatibel zur AGPL und die 3-Tage-Regel für npm.
- **Kiosk:** ein neuer Zugangsweg ohne Session und ohne zweiten Faktor; er darf nur genau das öffnen, was das Dashboard
  zeigt.

## Optionen

### Modell

1. **Freies JSON** (wie Grafana), im Server nur als Blob gespeichert. Einfach, aber jede Prüfung wäre Handarbeit, die UI
   müsste fremde Felder ertragen.
2. **Protobuf-Nachricht `DashboardSpec`** mit protovalidate-Grenzen; das JSON von Export und Import ist ihr
   kanonisches JSON (`protojson`), streng gelesen (unbekannte Felder abgelehnt). Ein Schema für API, Import und UI-Typen
   (D26).

### Abfragen der Widgets

1. **Eigene RPC `QueryWidget`** für alle Widgets. Ein neuer Pfad mit eigenen Grenzen.
2. **Die vorhandenen Leser** (`MetricsService.QueryMetrics`, `FleetService.GetFleetSummary`, `LogService.QueryLogs`,
   `AlertService.ListAlerts`) mit den Rechten und Grenzen des Betrachters. Nur der Kiosk braucht einen eigenen Eingang,
   weil er keine Session hat.

### Grid-Editor

| Kandidat | Lizenz | Größe (gzip) | Svelte 5 | Tastatur | CSP |
|---|---|---|---|---|---|
| `gridstack` 14.1 | MIT | ~26 KiB | Wrapper nötig | eigene, begrenzt | setzt Styles und Stylesheets selbst |
| `svelte-grid-extended` 1.2 | MIT | ~4 KiB | nein (Peer Svelte 4, seit 2024 ohne Release) | nein | `style`-Attribute |
| `@neodrag/svelte` 2.3 | MIT | ~2 KiB | ja | nur Ziehen, kein Grid, keine Größe | CSSOM |
| **eigenes Grid auf CSS Grid** | AGPL (eigener Code) | ~4–6 KiB geschätzt | ja | von Anfang an | CSSOM (`style:`-Direktive) |

Alle Fremdpakete erfüllen die 3-Tage-Regel; entscheidend sind CSP, Tastatur und Größe.

### Kiosk-Token

1. **Token in der URL als Query** (`?token=`) – landet in Logs von Proxys und im Verlauf.
2. **Token im Fragment** (`/kiosk#t=…`), die UI schickt ihn als Header; der Server sieht ihn nie in einer URL.
3. **Kiosk mit einer normalen Session** eines Service-Users – braucht Passwort und zweiten Faktor am Fernseher, die
   Session läuft nach 12 h ab.

## Entscheidung

### Modell (Option 2)

`sermon.web.v1.DashboardSpec`, Version `1`:

```json
{
  "version": 1,
  "title": "Web-Server",
  "description": "",
  "timeRange": "TIME_PRESET_1H",
  "variables": [{ "name": "node", "kind": "VARIABLE_KIND_NODE", "default": "<node-uuid>" }],
  "widgets": [
    {
      "id": "cpu",
      "kind": "WIDGET_KIND_TIMESERIES",
      "title": "CPU",
      "position": { "x": 0, "y": 0, "w": 6, "h": 4 },
      "query": { "metric": "sermon_cpu_utilization_ratio", "target": { "variable": "node" } }
    }
  ]
}
```

- **Layout:** 12 Spalten, Position `x` 0–11, `y` 0–99, Breite 1–12 (`x + w ≤ 12`), Höhe 1–12 Zeilen; Widgets
  überlappen nicht (der Server prüft das, der Editor schiebt Kollisionen nach unten).
- **Widgets:** `TIMESERIES`, `GAUGE`, `STAT`, `TABLE` (eine Metrik, eine Zeile je Node und Serie), `LOG_STREAM`,
  `ALERT_LIST`. Je Widget eine ID (`^[a-z0-9_-]{1,32}$`, eindeutig), ein Titel und genau eine Abfrage:
  Metrik-Widgets eine Metrik aus dem Katalog mit Label-Filter (wie `MetricSelector`), Log-Stream ein Suchtext
  (höchstens 200 Zeichen, wie im Log-Explorer), Alert-Liste optional Schweregrade.
- **Ziel jeder Abfrage:** genau eines von festen Nodes (höchstens 20), einer Gruppe oder einer Variablen.
- **Variablen:** höchstens 4, Art `NODE` oder `GROUP`, Name `^[a-z][a-z0-9_]{0,31}$`, optional ein Vorgabewert; die
  Auswahl steht in der URL (`?var-node=…`) und wird nicht gespeichert.
- **Zeit:** ein Vorgabe-Preset (15 min bis 30 Tage, wie §17); auf der Seite gilt der globale, synchronisierte
  Zeitbereich (`timeRange`), Zoom in einem Chart zoomt alle.
- **Version:** Der Server liest nur Versionen, die er kennt (heute `1`); eine neue Version bekommt einen Umsetzer im
  Server, alte Dokumente bleiben lesbar.

### Grenzen

| Was | Grenze | Warum |
|---|---|---|
| Widgets je Dashboard | 30 | jede Aktualisierung höchstens 30 Leser-Aufrufe |
| Nodes je Widget (fest) | 20 | `QueryMetrics` erlaubt 100, eine Gruppe ist auf 100 begrenzt |
| Variablen | 4 | |
| Dokument (JSON beim Import) | 32 KiB | unter der Grenze von 64 KiB je Request |
| Titel / Beschreibung | 200 / 2000 Zeichen, ohne Steuerzeichen | wie andere Namen (`naming.Clean`) |
| Dashboards je Org | 200 | |
| Kiosk-Tokens je Dashboard | 10 offene | |
| Aktualisierung | höchstens alle 30 s (Kiosk) bzw. beim Wechsel des Zeitbereichs | 30 Widgets ≙ 1 Aufruf/s |

Die Leser behalten ihre eigenen Grenzen (2000 Punkte je Serie, 100 Nodes, `HeavyReads`, Logs 1000 Zeilen).

### Rechte

- Jedes Mitglied der Org legt **eigene** Dashboards an; sie sind **privat** (nur der Eigentümer sieht sie) oder in der
  Org **geteilt** (alle Mitglieder sehen sie).
- **Ändern und löschen** darf der Eigentümer; geteilte Dashboards anderer zusätzlich ein Admin für alle Gruppen (neue
  Aktion `manage_dashboards`, org-weit). Ein Admin sieht private Dashboards anderer nicht.
- **Daten** zeigt ein Widget immer nur mit den Rechten des Betrachters: Die UI ruft die vorhandenen Leser mit seiner
  Session (Option 2), Nodes außerhalb seines Scopes sind `NOT_FOUND` und das Widget sagt „keine Daten oder kein Zugriff“.
  Das Dokument selbst (Titel, Node-IDs, Metriken) sehen alle Mitglieder eines geteilten Dashboards – es enthält keine
  Messwerte.
- **Speichern und Import** prüfen jede genannte Node- und Gruppen-ID: Sie muss in der Org sein und für den Speichernden
  sichtbar (`view_nodes`); sonst wird das Dokument abgelehnt und das Widget genannt, ohne zu sagen, ob die ID fremd oder
  unbekannt ist.
- **Step-up:** Dashboards anlegen, ändern, teilen und löschen brauchen keinen (eine Ansicht, keine neuen Rechte; die
  Leser prüfen jeden Betrachter). Einen Kiosk-Token anlegen braucht einen (ein neuer Zugang ohne Session); widerrufen
  nicht.
- **Audit:** Anlegen, Ändern, Teilen, Import, Löschen, Kiosk-Token anlegen und widerrufen.
- **Gleichzeitiges Ändern:** Jedes Dashboard hat eine Revision; ein Speichern mit veralteter Revision wird abgelehnt
  (`ABORTED`), die UI lädt neu.

### Speicher

Tabelle `dashboards` (Org, Eigentümer, geteilt, Revision, Dokument als `jsonb` im Format des Servers) mit `ENABLE` +
`FORCE ROW LEVEL SECURITY` und Policy auf `current_org_id()` (ADR 0007). Die Sichtbarkeit privater Dashboards prüft
zusätzlich jede Abfrage (`owner_id = $user OR shared`), mit Integrationstests.

### Import und Export

Export liefert das kanonische JSON des Modells ohne ID, Eigentümer und Revision. Import nimmt JSON bis 32 KiB, liest es
streng (`protojson`, unbekannte Felder abgelehnt), prüft Modell und IDs wie beim Speichern und legt ein neues, privates
Dashboard des Aufrufers an.

### Grid-Editor (eigenes Grid)

Ein eigenes Grid auf CSS Grid (`grid-column`/`grid-row` über die `style:`-Direktive, also CSSOM statt
`style`-Attribut – die CSP bleibt ohne neue Hashes):

- **Maus und Touch:** Pointer Events zum Verschieben und an einem Griff zum Ändern der Größe, Raster in ganzen Zellen.
- **Tastatur:** Jedes Widget ist im Editor fokussierbar; Pfeiltasten verschieben um eine Zelle, Umschalt + Pfeiltasten
  ändern die Größe, `Entf` entfernt (mit Rückfrage); jede Änderung wird über eine Live-Region angesagt
  („CPU: Spalte 3, Zeile 2, 6 × 4“).
- **Layout-Logik** als reine Funktionen (`move`, `resize`, Kollisionen nach unten schieben, nach oben verdichten) mit
  Unit-Tests; der Server prüft dieselben Regeln.
- Kein neues npm-Paket, keine fremde Lizenz; die Widgets laden ihren Code bei Bedarf (`import()`), die Seiten
  `/dashboards` und `/dashboards/[id]` bekommen eigene Fälle im Budget-Test.

### Kiosk-Token (Option 2, Details M5-26)

- **Format:** `smk_` + 32 Byte aus `crypto/rand` (256 Bit) in Base64url. Gespeichert nur SHA-256; gesucht über eine
  `SECURITY DEFINER`-Funktion (wie Enrollment-Tokens, ADR 0009), danach Vergleich in konstanter Zeit.
- **Bindung:** an genau ein geteiltes Dashboard und seine Org, mit Ablauf (1 bis 365 Tage, Vorgabe 90) und Namen.
  Widerrufen, das Dashboard löschen oder nicht mehr teilen beendet ihn. Mehrere Dashboards im Wechsel heißen mehrere
  Tokens im Fragment (`/kiosk#t=…,…`).
- **Anlegen:** nur Admins für alle Gruppen (neue Aktion `manage_kiosk_tokens`), mit Step-up und Audit; der Token wird
  einmal angezeigt.
- **Rechte beim Lesen:** die aktuellen Rechte des Anlegenden in der Org, bei jeder Anfrage neu geladen. Verliert er die
  Mitgliedschaft oder die Admin-Rolle für alle Gruppen, oder wird die Org gesperrt, ist der Token wirkungslos.
- **Eingang:** ein eigener `KioskService` (Header `Authorization: Kiosk <token>`, kein Cookie, keine Session) mit nur
  zwei Aufrufen: das Dokument des Dashboards und die Daten **eines Widgets dieses Dokuments**. Die Abfrage stellt der
  Server aus dem gespeicherten Widget zusammen (Zeitbereich und Variablen aus den Vorgaben), der Client nennt nur die
  Widget-ID; ausgeführt wird sie über dieselben Leser wie in der UI. Alle anderen Dienste kennen den Token nicht.
- **Grenzen:** Rate-Limit je Token und für fehlgeschlagene Versuche je IP; Log-Widgets nur, wenn die Rechte des
  Anlegenden Logs erlauben (Viewer-Schalter M3-12 gilt).

## Konsequenzen

- **Positiv:**
  - Ein Schema für API, Import und Export; der Server prüft jedes Dokument vollständig.
  - Kein neuer Abfragepfad in der UI: Scope, Grenzen und Rate-Limits der Leser gelten unverändert.
  - Der Editor kostet kein Fremdpaket, hält die CSP und ist von Anfang an per Tastatur bedienbar.
  - Der Kiosk sieht nur, was sein Dashboard zeigt; ein gestohlener Token gibt keine freien Abfragen.
- **Negativ / Risiken:**
  - Ein eigenes Grid ist Code, den wir pflegen (Kollisionen, Touch). Gegenmaßnahme: reine Layout-Funktionen mit Tests,
    bewusst ohne verschachtelte Grids und ohne freie Pixelpositionen.
  - Node-IDs anderer Gruppen in einem geteilten Dashboard sind für alle Mitglieder sichtbar (nicht ihre Daten). Ein
    Import aus einer anderen Instanz scheitert an fremden IDs; Variablen statt fester Nodes machen Dashboards
    übertragbar.
  - Ein Kiosk-Token ist ein langlebiges Geheimnis ohne zweiten Faktor. Gegenmaßnahmen: nur lesend, nur ein Dashboard,
    Ablauf, Widerruf, Audit, Rate-Limits; der Fernseher sieht genau das, was ohnehin im Flur hängt.
- **Security-Auswirkungen:** neuer Zugangsweg „Kiosk ↔ Server“ im Threat Model (M5-26); Tabellen der
  Tenant-Isolation und des Step-up für alle neuen RPCs; eine Abdeckungstabelle prüft, dass ein Kiosk-Token an keinem
  anderen Dienst etwas öffnet.
- **Folgearbeiten:** M5-23 (Server), M5-24 (Editor), M5-25 (Widgets und Variablen), M5-26 (Kiosk-Token), M5-27
  (Kiosk-Modus); die Vergleichsansicht (M5-28) bleibt eine feste Ansicht.
