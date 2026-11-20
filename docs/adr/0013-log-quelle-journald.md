# 0013 – Log-Quelle journald: `journalctl` als Kindprozess, eigener Cursor

- Status: accepted
- Datum: 2026-11-20
- Bezug: KONZEPT.md §2 (D17), §4.1, §10.1, §11; IMPLEMENTIERUNGSPLAN M0-10, E3.1, E2.3; ADR 0002, 0016

## Kontext

Der Agent soll das systemd-Journal lesen (Logs, Security-Events). Gegen `libsystemd` zu linken scheidet aus, die
musl-Binary bleibt statisch (ADR 0002). KONZEPT §4.1 sah `journalctl -o json -f --cursor-file=…` als Kindprozess vor.
Der Spike M0-10 (`spikes/journald/`) hat das in Containern mit systemd als PID 1 geprüft: Debian 13 (systemd 257),
Ubuntu 24.04 (255), Rocky 9 (252) und zusätzlich Rocky 8 (239), weil RHEL 8 noch lange im Einsatz ist.

## Ergebnis des Spikes (2026-11-20)

| Prüfung | Debian 13 | Ubuntu 24.04 | Rocky 9 | Rocky 8 |
|---|---|---|---|---|
| `--cursor-file` | ✓ | ✓ | Datei nach SIGTERM **nicht** geschrieben, 5 Duplikate | **unbekannte Option** |
| Cursor-Datei nach SIGKILL | unverändert → Duplikate | unverändert → Duplikate | – | – |
| `--after-cursor=<__CURSOR>` nach Neustart | ✓ 0 Duplikate | ✓ | ✓ | ✓ |
| Folgen über `--rotate` und Neustart von journald | ✓ | ✓ | ✓ | ✓ |
| Cursor nach `--vacuum` gelöscht | setzt am nächsten Eintrag fort, Rückgabecode ok, gelöschte Einträge fehlen | gleich | gleich | gleich |
| `__SEQNUM` (Lücken erkennen) | ✓ | ✓ | fehlt | fehlt |
| Felder > 4 KiB ohne `--all` | `null` | `null` | `null` | String |
| Binärer `MESSAGE` | Array von Bytes | Array | Array | **rohe Bytes, ungültiges UTF-8 im JSON** |
| Erster Start ohne Cursor | letzte 10 Einträge | gleich | gleich | gleich |
| Latenz Journal → Pipe (Median) | 447 ms | 445 ms | 444 ms | 446 ms |
| RSS leer / unter Last | 7,1 / 15,4 MiB | 6,7 / 15,6 MiB | 7,7 / 13,5 MiB | 6,1 / 16,0 MiB |
| CPU je Zeile (20 000 Zeilen) | 20 µs | 22 µs | 20 µs | 10 µs |
| Lesen als User in `systemd-journal` | ✓ (ohne Gruppe: nichts) | ✓ | ✓ | ✓ |

Weitere Beobachtungen:

- `__CURSOR`, `__REALTIME_TIMESTAMP` und `__MONOTONIC_TIMESTAMP` sind immer dabei, auch mit `--output-fields`. Zahlen
  kommen als Strings, `PRIORITY` z. B. als `"4"`.
- Im Cursor steht die Sequenznummer auch bei alten Versionen (`s=<seqnum-id>;i=<seqnum hex>;…`); damit lassen sich
  Lücken ohne `__SEQNUM` erkennen. Der Cursor ist offiziell undurchsichtig, die Auswertung ist deshalb nur ein Hinweis.
- `_CMDLINE` enthält Kommandozeilen im Klartext, im Spike das Passwort aus `su alice -c "echo … | sudo -S"`. Die
  Redaction im Agent (KONZEPT §10.3) muss auch Felder wie `_CMDLINE` erfassen oder sie gar nicht übernehmen.
- Ab Debian 13 heißt der Absender der SSH-Meldungen `sshd-session` statt `sshd` (Parser in E2.3 beachten).
- Rund 40 000 Zeilen/s (Rocky 8: 70 000) liest ein `journalctl`-Prozess ohne Mühe; für den Agent ist das Parsen der
  teurere Teil.

## Optionen

1. **`--cursor-file`** (wie im Konzept): unzuverlässig (Rocky 9 schreibt nichts, SIGKILL hinterlässt alte Stände),
   auf systemd < 242 nicht vorhanden; der Stand ist nicht an den WAL gekoppelt.
2. **Eigener Cursor:** Der Agent liest `__CURSOR` aus jedem Eintrag, speichert ihn, sobald der Eintrag im WAL liegt,
   und startet mit `--after-cursor`. Funktioniert ab systemd 239.
3. **`libsystemd` per FFI oder eigener Journal-Leser:** bricht die statische Binary bzw. bedeutet, das
   Journal-Dateiformat nachzubauen.

## Entscheidung

Gewählt: **Option 2.** Der Agent startet

```
journalctl -o json --all --follow --output-fields=<feste Liste> (--after-cursor=<gespeichert> | --lines=0)
```

- **Cursor im Agent:** Der Cursor des letzten Eintrags, der im WAL liegt, wird atomar im State-Verzeichnis gespeichert
  (höchstens etwa jede Sekunde). Nach Absturz oder SIGKILL kommen höchstens die Einträge seit der letzten Speicherung
  erneut (at-least-once, wie bei Metriken); `--cursor-file` wird nicht verwendet.
- **Erster Start** ohne gespeicherten Cursor mit `--lines=0`: keine Altlasten, kein Nachliefern vergangener Tage.
- **`--all`**, damit Felder über 4 KiB nicht als `null` ankommen; der Agent kürzt selbst (16 KiB pro Zeile, §10.2).
- **Feste Feldliste** (`--output-fields`): `MESSAGE`, `PRIORITY`, `SYSLOG_IDENTIFIER`, `SYSLOG_FACILITY`,
  `_SYSTEMD_UNIT`, `_PID`, `_COMM`, `_BOOT_ID`, `_TRANSPORT`, `_UID`; `_CMDLINE` und Umgebung werden nicht gelesen.
- **Robustes Parsen:** jede Zeile einzeln; ungültiges UTF-8 wird vor dem JSON-Parser ersetzt (U+FFFD), Werte können
  String, Byte-Array oder `null` sein; Zeilenlänge begrenzt. Eine kaputte Zeile kostet nur diese Zeile.
- **Lücken** (Cursor nach `--vacuum` oder Journal-Rotation verloren) erkennt der Agent über `__SEQNUM` bzw. die
  Sequenznummer im Cursor und meldet sie als Zähler (Metrik und Hinweis in der UI), statt still zu schweigen.
- **Kindprozess überwachen:** Endet `journalctl`, startet der Agent ihn mit Backoff neu (ab dem gespeicherten Cursor);
  stderr wird begrenzt ins Agent-Log übernommen. Der Prozess läuft mit den Rechten des Agents (Gruppe
  `systemd-journal`, Unit aus ADR 0012).
- **Latenz** von rund 0,45 s ist für Logs und Security-Events ausreichend.

## Konsequenzen

- Positiv: Funktioniert auf allen Zielsystemen mit systemd ab 239, ohne Duplikate beim normalen Neustart und mit
  begrenzten Duplikaten nach einem Absturz; der Agent bleibt statisch; Lücken werden sichtbar.
- Negativ: Das JSON von `journalctl` ist nicht immer gültig (Rocky 8) und muss tolerant gelesen werden; ein
  Kindprozess mehr pro Agent (rund 7 MiB RSS) zusätzlich zum Ressourcenziel von 30 MB des Agents selbst.
- Security: Logzeilen sind nicht vertrauenswürdige Eingaben (Parser werden gefuzzt, E2.3/E3.1); Kommandozeilen werden
  nicht übernommen; ohne Gruppe `systemd-journal` sieht der Agent nur eigene Einträge.
- Folgearbeiten: Umsetzung in E3.1 (Log-Pipeline) und E2.3 (Security-Events) mit den Fixtures aus
  `agent/fixtures/logs/`; KONZEPT §4.1 und §10.1 sind angepasst.
