# 0019 – Alerting: Kanäle, statistische Anomalien, Regeln in UI und YAML

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D14, D16, D22), §12, §13, §18.6

## Kontext

Alerts sind der Teil von SerMon, der Menschen weckt. Sie müssen dort ankommen, wo das Team arbeitet, dürfen nicht
flattern und müssen nachvollziehbar sein. Feste Schwellen passen nicht zu jedem Server; Machine-Learning-Modelle
sind schwer zu erklären und zu testen. Regeln sollen sich versionieren lassen.

## Optionen

1. **Kanäle:** nur E-Mail und Webhook (Rest über Dritte) oder die gängigen Chat- und Push-Dienste direkt.
2. **Anomalien:** keine, ML-Modelle (Training, schwer erklärbar) oder statistische Verfahren mit Baselines.
3. **Regeln:** nur UI, nur Dateien, oder UI mit YAML-Import/-Export.

## Entscheidung

- **D14 – Kanäle direkt eingebaut:** E-Mail (SMTP mit TLS-Pflicht), Webhook (HMAC-signiert), Slack, Teams, Discord,
  Mattermost, Telegram, ntfy, Gotify, Pushover. Ein gemeinsames `Notifier`-Interface, „Test senden“, verschlüsselte
  Zugangsdaten, SSRF-Schutz für alle ausgehenden Requests.
- **D16 – Anomalien statistisch und serverseitig:** robuster Z-Score gegen saisonale Baselines (Median/MAD je Stunde
  der Woche) und EWMA; anomal nur, wenn beide zustimmen und es mehrere Minuten anhält; 7 Tage Lernphase.
- **D22 – Regeln in der UI und als YAML** (Import/Export, gleiche Validierung); Standardregeln werden mitgeliefert.

## Konsequenzen

- Positiv: Benachrichtigungen ohne Zusatzdienste; Anomalien sind erklärbar („5× höher als dienstags um diese Zeit“)
  und mit synthetischen Reihen golden-testbar; Regeln lassen sich per Git verwalten.
- Negativ: Jeder Kanal ist Code, der gepflegt werden muss; statistische Verfahren erkennen keine komplexen Muster.
- Security: Ausgehende Requests nur nach SSRF-Prüfung (keine internen Ziele ohne Freigabe); Webhook-Signaturen mit
  Zeitstempel gegen Replay.
- Folgearbeiten: Alert-Engine, Kanäle und Regeln in M2, Anomalien in M4; weitere Detektoren hinter dem Interface
  `Detector` in v2.

## Nachtrag M2-02: Ausführung der Engine

- Jede Server-Instanz versucht alle 10 s, die Orgs mit Regeln zu leasen (Compare-and-swap auf `alert_leases`, 30 s,
  in der Tenant-Transaktion der Org). Nur der Halter wertet aus; fällt er aus, übernimmt eine andere Instanz nach
  Ablauf. Das erfüllt „Leases in PG“ aus dem Plan, ohne eine Transaktion über die ganze Auswertung offen zu halten.
- Zustand und Historie liegen in PostgreSQL mit RLS; die Historie ist für die Anwendung nur anhängbar. Wieder ruhige
  Instanzen (OK ohne Flapping-Historie) werden gelöscht.


## Nachtrag M2-04: Regelformat, Standardregeln und Overrides

- **Schema aus dem Proto:** Die Regel ist die Nachricht `sermon.web.v1.AlertRule`. Sie liegt im Web-Paket, damit der
  Regel-Editor (M2-17) sie als TypeScript bekommt; der Agent braucht sie nicht. YAML wird mit `go.yaml.in/yaml/v3`
  gelesen, als JSON strikt per `protojson` übernommen und mit protovalidate geprüft. Unbekannte Felder meldet ein
  eigener Durchlauf über den YAML-Baum mit Zeile und Pfad, denn `protojson` kennt nur die Position im JSON.
- **Typprüfung über den Katalog:** Felder eines anderen Regeltyps sind Fehler, ebenso Verhältnisse außerhalb von 0..1.
  So scheitert „85“ statt „0.85“ beim Laden statt still nie auszulösen.
- **Standardregeln eingebettet:** `rules/` ist die Quelle. `just gen-rules` kopiert sie in den Server, `check-gen`
  hält die Kopie aktuell (wie bei `install.sh`). Kein Laden zur Laufzeit von der Platte.
- **Overrides als ganze Dokumente:** Pro Org und Regel speichert `alert_rule_overrides` (RLS) „abgeschaltet“ und/oder
  ein vollständiges Regeldokument; eigene Regeln sind Overrides ohne Standardregel. Feldweise Patches wurden verworfen:
  Sie brechen still, wenn sich die Standardregel ändert. Ein Override, das nicht mehr lädt, wird protokolliert und
  ignoriert, damit die Org die Regel nicht verliert.
- **Ende von Alerts:** Gilt eine Regel für eine Instanz nicht mehr, setzt die Engine einen feuernden Alert auf
  `RESOLVED` (mit Historie, damit die Notifier „behoben“ melden) und löscht die Instanz. Eine Regel, deren Auswertung
  fehlschlägt, behält ihre Alerts.
- **Last:** Fenster über 1 h lesen mit grobem Schritt (24 h → 5 min) aus dem 1-min-Aggregat statt aus den Rohdaten.
  Zustandsregeln holen den aktuellen Wert getrennt aus den letzten 5 min.

## Nachtrag M2-08: Event-Regeln

- **Lesen statt Strom:** Ingest und Auswertung einer Org laufen auf verschiedenen Instanzen (M2-07). Jede Runde liest
  deshalb die Events, die im Zählfenster **angekommen** sind (`received_at`, Index `(org_id, received_at, id)`), und
  zählt sie nach ihrer **Entstehung** (`occurred_at`): das Maximum an Events innerhalb eines Fensters (halb offen).
  Ein Cursor wurde verworfen; er bräuchte Zustand im Speicher, der bei der Übernahme des Leases verloren geht, und
  wegen der Commit-Reihenfolge einen Abstand zu `now()`. Das gleitende Fenster liest jedes Event mehrfach, ist aber
  zustandslos.
  - Ein Rückstau nach einem Ausfall des Agents löst beim Eintreffen aus, wenn er einen Burst enthält; ein Tag
    verstreuter Fehlversuche, der auf einmal ankommt, löst nicht aus.
  - Ein Alert hält, solange die Zahl im Fenster reicht, und löst sich danach. Das Fenster ersetzt `clear`.
- **Instanzen:** Regel × Node × `groupBy` (`ip`, `user`). **IPv6-Adressen zählen pro /64**, sonst entgeht ein
  Angreifer der Schwelle, indem er Adressen in seinem Netz rotiert. Für die Fleet-Korrelation (`count.of: nodes`)
  bekommt jeder beteiligte Node eine Instanz; eine Org-Instanz ohne Node hätte das Schema (Fremdschlüssel auf
  `nodes`) und die Node-Ansicht gebrochen. Die Korrelation sieht nur die Nodes der eigenen Org (RLS).
- **Offene Instanzen ohne Events** meldet die Regel als 0, dafür bekommt sie die offenen Instanzen der Engine.
- **Grenzen gegen Fluten:** höchstens 50 000 Events pro Abfrage (die neuesten), höchstens 100 Instanzen pro Regel und
  Runde (die höchsten Zahlen zuerst). Bei einer Kappung behalten offene Instanzen ihren Zustand, statt auf einer
  unvollständigen Zahl zu enden. Zähler `sermon_server_alert_event_reads_truncated_total`. Regeln derselben Art und
  desselben Fensters teilen sich eine Abfrage pro Runde.
- **Felder:** `notifyFields` kopiert Felder des neuesten Events (`ts`, `user`, `ip`, `port`, …) in die Transition und
  die Instanz, gespeichert als `fields` (jsonb, Migration 00023). Die Werte wählt ein Angreifer: Steuerzeichen werden
  zu U+FFFD, jeder Wert endet nach 256 Bytes. Dasselbe gilt für Dimensionen aus User-Namen.
- **Neue Felder im Schema:** `event`, `match` (nur gleich, z. B. `user: root`), `groupBy`, `count` (`window`,
  `warning`, `critical`, `of`), `after` (vorausgehende Events: Art, Anzahl, Fenster; gleicher Node und gleiche
  `groupBy`-Werte) und `notifyFields`. Welche Felder eine Art hat, legt `secevent.FieldsOf` fest; der Loader prüft
  dagegen.

## Nachtrag M2-10: Notifier-Framework

- **Warteschlange in PostgreSQL:** `notification_deliveries` ist Queue und Zustellprotokoll zugleich, mit RLS wie alle
  Tenant-Tabellen. Jede Server-Instanz holt fällige Zeilen pro Org mit `FOR UPDATE SKIP LOCKED` und einem Claim von
  2 min. Fällt eine Instanz beim Senden aus, übernimmt nach Ablauf des Claims eine andere. Das ergibt
  „mindestens einmal“: Eine Nachricht kann doppelt ankommen, geht aber nicht verloren. Eine Queue im Speicher hätte
  bei einem Neustart Benachrichtigungen verloren; ein eigener Broker widerspräche dem einfachen Betrieb.
- **Retries:** höchstens 6 Versuche (nach 30 s, 1, 2, 4, 8 min), `Retry-After` bei 429 bis 1 h. Dauerhafte Fehler
  scheitern sofort: 4xx außer 408 und 429, gesperrte Ziele aus `safehttp` und gelöschte oder abgeschaltete Kanäle.
- **Aufbewahrung:** Das Protokoll enthält die Felder der Alerts (User, IP) und wird nach 30 Tagen gelöscht.
  Die API zeigt daraus nur Art, Regel und Node.
- **Zugangsdaten:** pro Kanal als JSON mit `secrets.Box` versiegelt, Kontext `notification_channel:<org>:<id>`.
  Sie sind nur schreibbar; ein Update ersetzt nur die genannten. Fehlertexte werden von den Zugangsdaten des Kanals
  bereinigt, falls ein Dienst sie zurückgibt.
- **Deep-Links** zeigen auf den ersten Eintrag von `SERMON_ALLOWED_ORIGINS`, bis M2-16 auf `/nodes/<id>`.
- **Noch nicht verbunden:** Die Engine liefert ihre Transitionen erst mit dem Routing (M2-14) an
  `Dispatcher.Enqueue`. Die Kanaltypen folgen ab M2-11; bis dahin ist die Registry leer.

## Nachtrag M2-11: E-Mail, Webhook, ntfy

- **Webhook-Signatur** `t=<Unix-Sekunden>,v1=<hex HMAC-SHA256 über "<t>.<Body>">`: Der Zeitstempel ist signiert, ein
  Empfänger lehnt alte Requests ab (Replay). Das Verfahren ist das von Stripe, für das Empfänger oft schon Code haben.
  Gesendet wird nie ein Token im Klartext.
- **ntfy:** Das Topic ist ein Geheimnis wie das Token, weil auf öffentlichen Servern jeder mitliest, der es kennt.
  Gesendet wird JSON an die Wurzel des Servers statt Header (`Title`, `Priority`). So muss kein Wert mit
  Nicht-ASCII-Zeichen in einen Header, und Markdown bleibt aus.
- **E-Mail:** eine Mail pro Empfänger statt einer mit allen in `To`. Die Empfänger sehen sich nicht, und eine
  abgelehnte Adresse hält die anderen nicht auf. Bei einem Retry kann ein Empfänger, der die Mail schon hat, sie
  noch einmal bekommen; das passt zu „mindestens einmal“. Der Mailserver ist der des Betreibers, darum läuft E-Mail
  nicht über `safehttp`.
