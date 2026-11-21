# 0025 – Logdateien als Quelle ohne journald: auswerten, aber markieren

- Status: accepted
- Datum: 2026-11-22
- Bezug: KONZEPT.md §10.1, §11, §18; ADR 0013, ADR 0023; IMPLEMENTIERUNGSPLAN M2-20

## Kontext

Alpine hat kein journald. Die Security-Meldungen (sshd, sudo, useradd, Kernel) landen bei busybox `syslogd` in
`/var/log/messages`, bei rsyslog oder syslog-ng in `/var/log/auth.log` oder `/var/log/secure`. ADR 0023 wertet nur
Zeilen privilegierter Absender aus; dafür braucht es `_UID` und `_CAP_EFFECTIVE`, die journald aus `/proc` setzt.
Klassische Logdateien haben diese Felder nicht.

Versuche in Alpine-Containern (`agent/fixtures/logs/capture/capture-alpine.sh`):

- **busybox syslogd:** `logger -p auth.info -t sshd "Accepted password for root from …"` eines normalen Users ist von
  einer echten sshd-Zeile nicht zu unterscheiden.
- **rsyslog mit `imuxsock` und `ParseTrusted`:** rsyslog bekommt die UID des Absenders vom Kernel (`SO_PASSCRED`).
  sshd, sshd-session und useradd schreiben mit UID 0, die Fälschung mit 1000. **sudo** schreibt aber mit der realen UID
  des aufrufenden Users (setuid) und ohne `exe`: echte sudo-Zeilen sehen aus wie Fälschungen.
- busybox syslogd läuft unter OpenRC mit Gruppe `wheel` und umask 027; der Agent (User `sermon`) kann die Dateien
  nicht lesen. `wheel` ist die Admin-Gruppe (doas, sudo).
- Das sudo von Alpine nutzt kein PAM und schreibt nur `alice : 1 incorrect password attempt ; …`.
- Alpine hat ohne systemd und dbus kein `/etc/machine-id`.

## Entscheidung

**Ohne journald liest der Agent eine Logdatei. Events daraus zählen, sind aber als „nicht verifiziert“ markiert,
außer rsyslog nennt root als Absender.**

- **Wahl der Quelle:** Gibt es `/run/systemd/journal`, nimmt der Agent das Journal (ADR 0013). Sonst die erste
  vorhandene Datei aus `/var/log/sermon/security.log` (SerMon-Vorlage für rsyslog), `/var/log/auth.log`,
  `/var/log/secure`, `/var/log/messages`. Das Format gehört zur Datei und wird nie aus der Zeile geraten: Eine
  gefälschte Zeile kann kein anderes Format vortäuschen.
- **SerMon-Vorlage für rsyslog:** `%timegenerated:::date-rfc3339% %inputname% uid=%$!uid% %syslogtag%%msg%`. Zeit,
  Eingang und UID stehen vor allem, was der Absender bestimmt. Die Konfiguration steht im Hilfetext
  `concepts/unverified-source`.
- **Vertrauensregel für Dateien:**

  | Absender laut Datei | sshd, useradd, userdel, Kernel | sudo |
  |---|---|---|
  | unbekannt (busybox, klassisch, aus dem Netz) | auswerten, nicht verifiziert | auswerten, nicht verifiziert |
  | UID 0 bzw. `imklog` | verifiziert | verifiziert |
  | andere UID | verwerfen | auswerten, nicht verifiziert |

- **Markierung durchgehend:** Agent → `SecurityEvent.unverified` (Feld 13; ältere Agents lesen nur das Journal und
  lassen es `false`) → Spalte `security_events.unverified` → Web-API. Ist **eines** der gezählten Events eines Alerts
  nicht verifiziert, bekommt der Alert das Feld `source: unverified`, unabhängig von `notifyFields`: Eine gefälschte
  Zeile kann den Zähler (Brute-Force, Flotten-Korrelation) erhöht haben. Kanäle zeigen „Quelle: nicht verifiziert“ als
  erste Zeile der Felder, die UI einen Hinweis mit Icon, Text und ⓘ.
- **Leserecht:** `install.sh` ändert keine Systemdateien von selbst. Mit `--grant-log-access` schreibt es ein
  `start_pre` in `/etc/conf.d/syslog`, das busybox syslogd mit Gruppe `sermon` statt `wheel` startet (das Init-Skript
  setzt die Gruppe selbst, `conf.d` allein überschreibt sie nicht), startet syslog neu und setzt die Gruppe vorhandener
  Dateien. Ohne Flag und ohne lesbare Datei nennt `install.sh` beide Wege. Der Agent warnt einmal, wenn er die Datei
  nicht lesen kann.
- **Lesen:** Polling jede Sekunde; Rotation durch Umbenennen und `copytruncate`; Position (Gerät, Inode, Offset) wird
  wie der Journal-Cursor erst nach dem Schreiben in den WAL festgeschrieben. Nach einem Neustart sucht der Agent eine
  inzwischen rotierte Datei als `<name>.0` oder `<name>.1`. Beim ersten Start liest er ab dem Ende.
- **Zeit:** busybox schreibt lokale Zeit ohne Jahr. Die Zeitzone kommt aus `/etc/localtime` (über `ProcRoot`,
  ausgewertet mit `jiff` ohne eigene Zeitzonendatenbank), sonst UTC. Das Jahr ist das aktuelle, das vorige, wenn die
  Zeit mehr als einen Tag in der Zukunft läge.
- **sudo ohne PAM:** Die Zusammenfassung `N incorrect password attempt(s)` ergibt ein `sudo.auth.failed`, solange der
  Parser keine verifizierte PAM-Zeile gesehen hat. Mit PAM (journald) wiederholt sie nur die PAM-Zeilen.
- **machine-id:** Der Agent liest ersatzweise `/var/lib/dbus/machine-id`; fehlt beides, legt `install.sh`
  `/etc/machine-id` an (128 Zufallsbits, Modus 0444).

## Konsequenzen

- **Positiv:** Alpine liefert SSH-, sudo- und Konto-Events. Wer gefälschten Alerts nicht trauen will, sieht es sofort
  und kann mit rsyslog verifizieren. Für Hosts mit journald ändert sich nichts.
- **Negativ:**
  - Mit busybox syslogd kann jeder lokale User Alerts auslösen und fremde Adressen als Angreifer erscheinen lassen. Die
    Markierung verhindert das nicht, sie macht es sichtbar.
  - sudo-Events aus Dateien sind nie verifiziert.
  - Mit `--grant-log-access` lesen Mitglieder von `wheel` die Logs nur noch mit doas.
  - Auf einem System mit PAM und ohne journald zählen fehlgeschlagene sudo-Versuche doppelt (PAM-Zeile und
    Zusammenfassung sind beide nicht verifiziert).
  - Aus Dateien gibt es kein `system.reboot`: Boot-IDs liefert nur journald.
  - Zeilen, die während der Rotation zwischen dem letzten Lesen der alten Datei und dem Wechsel geschrieben werden,
    liest der Agent beim nächsten Durchlauf noch einmal aus der alten Datei; danach nicht mehr.
- **Security:** Die Datei-Quelle ist ein neuer Eingang für Daten, die jeder lokale User bestimmt. Sie läuft durch
  dieselben Parser (gefuzzt, auch mit `Source::File`), Zeilen sind auf 16 KiB, ein Lesevorgang auf 1 MiB begrenzt.
  Threat Model T70.
- **Folgearbeiten (M2-31):** Reboot-Erkennung ohne journald (Boot-ID beim Start des Agents vergleichen); Hinweis in
  der UI, wenn ein Node gar keine Security-Quelle hat.
