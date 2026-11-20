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
