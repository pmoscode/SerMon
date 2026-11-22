# 0023 – Vertrauen in Security-Events: nur von privilegierten Absendern

- Status: accepted
- Datum: 2026-11-20
- Bezug: KONZEPT.md §11, §18; ADR 0013; IMPLEMENTIERUNGSPLAN M2-06

## Kontext

Der Agent macht aus Journal-Zeilen Security-Events (SSH-Logins, sudo, Konten, OOM-Kills). Daraus entstehen Alerts:
Brute-Force pro IP, „dieselbe IP auf mehreren Nodes“, Root-Login. Später können daraus auch Reaktionen folgen, etwa
das Sperren einer IP.

Jeder lokale User kann aber Zeilen mit beliebigem Absendernamen ins Journal schreiben:

```
logger -t sshd "Failed password for root from 203.0.113.9 port 22 ssh2"
```

Ohne Prüfung würde SerMon so eine fremde IP als Angreifer melden, Alerts fluten oder echte Ereignisse überdecken. Die
Fixtures aus M2-06 enthalten solche gefälschten Zeilen von einem User ohne Rechte.

Was journald selbst setzt und der Absender nicht fälschen kann:

- `_UID` aus `SCM_CREDENTIALS`;
- `_CAP_EFFECTIVE`, `_EXE` und `_COMM`, gelesen aus `/proc/<pid>`;
- `_TRANSPORT`.

Die Captures zeigen:

| Absender | `_UID` | `_CAP_EFFECTIVE` |
|---|---|---|
| sshd/sshd-session (Login-Meldungen) | 0 | `1ffffffffff` |
| sudo (setuid root) | UID des Users, z. B. 1000 | `1ffffffffff` |
| useradd/userdel | 0 | `1ffffffffff` |
| `logger` eines normalen Users | 1000 | `0` oder fehlt (Prozess schon beendet) |

## Optionen

1. **Dem Absendernamen trauen:** einfach, aber fälschbar (siehe oben).
2. **`_UID = 0` verlangen:** Das schließt sudo aus, das mit der UID des Users loggt.
3. **`_EXE` prüfen** (`/usr/bin/sudo`): Pfade unterscheiden sich je Distro. Eine Kopie unter `~/sudo` hätte denselben
   Namen, und der Pfad fehlt, wenn der Prozess schnell endet.
4. **Privilegierter Absender:** `_UID = 0` oder `_CAP_EFFECTIVE ≠ 0`. Kernel-Meldungen (OOM) nur über
   `_TRANSPORT=kernel`.

## Entscheidung

Gewählt: **Option 4.**

- Zeilen von sshd, sshd-session, sshd-auth, sudo, useradd und userdel werden nur von einem privilegierten Absender
  ausgewertet. Ein normaler Prozess hat keine Capabilities; sie bekommt nur, wer root ist oder setuid/File-Capabilities
  hat, und die vergibt nur root.
- Fehlt `_CAP_EFFECTIVE` bei `_UID ≠ 0` (der Absender war schon beendet), wird die Zeile verworfen.
- OOM-Kills zählen nur mit `_TRANSPORT=kernel`. Der Name `kernel` als Absender reicht nicht.
- Der Agent liest dafür zusätzlich das Feld `_CAP_EFFECTIVE` (Hexadezimalzahl, kein Secret). Die feste Feldliste aus
  ADR 0013 wächst damit auf 11 Felder.

Dazu kommen Regeln gegen vom Angreifer gewählte Inhalte:

- **Adresse und Port vom Ende der Zeile:** Den User-Namen wählt der Angreifer. `Invalid user x from 192.0.2.66 port 22
  from 127.0.0.1 port 50516` ergibt 127.0.0.1.
- **Feste Längen:** User höchstens 128 Byte, Host höchstens 253, sudo-Befehl höchstens 1024, Prozessname höchstens 15
  Zeichen.
- **Keine Rohzeilen in Events.**
- **Muster mit der `regex`-Crate:** lineare Laufzeit, kein ReDoS.

## Konsequenzen

- **Positiv:** Ein User ohne Rechte kann keine SSH-, sudo- oder Konto-Events erzeugen. Die Regel ist für alle Absender
  gleich und hängt nicht von Pfaden ab.
- **Negativ:**
  - Endet ein privilegierter Absender, bevor journald `/proc` liest, und ist er nicht root, geht die Zeile verloren.
    Bei sudo trat das in den Captures nicht auf; sudo loggt, solange es läuft.
  - Container-Root (eigene User-Namespaces) hat Capabilities und gilt als privilegiert.
  - Klassische Logdateien (`/var/log/auth.log`, Alpine in M2-20) haben diese Felder nicht. Dort gilt das Vertrauen in
    den Syslog-Daemon.
- **Security:** root bleibt allmächtig und kann alles fälschen. Wer root hat, hat den Host ohnehin.
- **Folgearbeiten:** Die Quelle für Logdateien (M2-20) braucht eine eigene Begründung des Vertrauens.

## Nachtrag M2-20 (Logdateien)

Die Folgearbeit ist erledigt: [ADR 0025](0025-logdateien-ohne-journald.md). Ohne journald zählen Zeilen aus Logdateien,
sind aber als „nicht verifiziert“ markiert; verifiziert nur mit der SerMon-Vorlage für rsyslog und UID 0. Die
sudo-Zusammenfassung `N incorrect password attempts` zählt nur, solange keine verifizierte PAM-Zeile kam.

## Nachtrag M2-28 (Security-Review)

- **`_CAP_EFFECTIVE` ist kein Beleg für sshd, useradd und userdel.** journald nimmt `_UID` aus den Credentials des
  Sockets (`SCM_CREDENTIALS`), liest `_CAP_EFFECTIVE` aber erst bei der Verarbeitung aus `/proc/<pid>`
  (`systemd.journal-fields`: nicht vertrauenswürdig). Ein User ohne Rechte sendet eine gefälschte Zeile und führt sofort
  ein setuid-Programm aus (`passwd`); dieselbe PID hat dann volle Capabilities. Die Entscheidung oben betrachtete nur
  den umgekehrten Fall (Absender schon beendet).
- **Neu:** sshd, sshd-session, sshd-auth, useradd und userdel zählen nur mit `_UID` 0. Die Capabilities gelten nur
  noch für sudo, das setuid mit der UID des Users loggt.
- **Folgen:**
  - sudo-Zeilen bleiben über dieses Race fälschbar. Sie täuschen aber höchstens fehlgeschlagene oder abgelehnte
    sudo-Versuche des Users selbst vor.
  - sshd in Containern mit eigenen User-Namespaces (Root mit Capabilities, aber `_UID` ≠ 0 im Host-Journal) liefert
    keine Events mehr.
- **sudo-Ziel:** Das Arbeitsverzeichnis wählt der User, und sudo maskiert ` ; ` darin nicht. Nennt eine Zeile `USER=`
  mehrfach, ist das Ziel `?` statt des eingeschleusten.
- **Text vom Host:** Der Server ersetzt in Event-Feldern und Host-Fakten Steuer-, Format- (Bidi, Null-Breite) und
  Trennzeichen durch U+FFFD (`textsafe`); sshd maskiert Nicht-ASCII-Zeichen, der Agent dekodiert sie.

## Nachtrag M2-37 (Flut von außen)

- **Zwei Buckets pro Node im Ingest:** Fehlgeschlagene und ungültige Logins und Abbrüche vor der Anmeldung kann jeder aus
  dem Internet auslösen (`Kind.Noisy`); sie teilen sich den Bucket von 50/s (Burst 5 000). Alle anderen Arten
  (erfolgreicher Login, sudo, Konten, System) haben einen eigenen von 5/s (Burst 500). Vorher leerte eine Flut von
  Fehlversuchen den gemeinsamen Bucket, und der erfolgreiche Login danach ging verloren – genau das Event, das
  „Login nach Fehlversuchen“ und „Root-Login“ brauchen.
- **Sichtbar:** `sermon_server_ingest_dropped_security_events_by_kind_total{kind}` in `/metrics`; verworfene seltene
  Events meldet das Self-Monitoring als `dropped_security_events` (ADR 0027).
- **Verworfen: Zusammenfassen im Agent** (Fehlversuche pro IP und Minute als ein Event mit Anzahl). Das änderte das
  Protokoll und die Zählung der Event-Regeln (Fenster, `groupBy`), und die Buckets lösen das Problem schon: Den
  Brute-Force-Alert lösen die ersten Tausend Fehlversuche ohnehin aus.
