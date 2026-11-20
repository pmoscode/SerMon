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
