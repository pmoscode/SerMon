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
