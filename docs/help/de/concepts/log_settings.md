# Log-Einstellungen

Was die Agents an Logs senden, wer sie sehen darf und was vorher geschwärzt wird. Admins für alle Gruppen ändern es, jede Änderung fragt noch einmal nach dem Passwort.

## Viewer

Logs und Security-Events enthalten personenbezogene Daten (User-Namen, Adressen). Mit dem Schalter sehen Viewer sie nicht mehr; Operatoren und Admins schon. Er gilt ab der nächsten Anfrage jeder Sitzung, laufende Live-Ansichten enden.

## Pro Gruppe

- **Logs senden:** aus heißt, die Agents der Gruppe senden keine Logs. Security-Events kommen weiter.
- **Mindest-Schweregrad** und **ausgeschlossene Units** (systemd-Units oder Syslog-Kennungen) sparen Speicher und Lärm.
- **Logdateien:** absolute Muster mit `*` und `?`, z. B. `/var/log/nginx/*.log`. Ein Eintrag beginnt mit einer Zeile, auf die der reguläre Ausdruck passt (z. B. `^\d{4}-` für Zeilen mit Datum); ohne Ausdruck gehören eingerückte Zeilen zum Eintrag davor. Die Agents lesen nur unter den Verzeichnissen, die ihr Host erlaubt (`log_file_prefixes` in `agent.toml`, Standard `/var/log`) – nie ihre eigenen Schlüssel.

## Redaction

Die Agents ersetzen Passwörter, Tokens und private Schlüssel durch `***`, bevor eine Zeile den Host verlässt. Eigene Regeln kommen dazu: Ein regulärer Ausdruck ersetzt seinen Treffer, oder nur die Gruppe `(?P<secret>…)`. **Testen** zeigt an einer Beispielzeile, was die Agents mit den Regeln im Formular senden würden.

## Stand pro Node

Welche Version der Einstellungen jeder Agent angewendet hat. Ein Fehler heißt: Der Agent hat die Einstellungen abgelehnt (etwa einen regulären Ausdruck, den er nicht übersetzen kann) und arbeitet mit den vorherigen weiter. „allow_logs = false“ heißt: Der Host verbietet den Versand lokal, das gewinnt immer.
