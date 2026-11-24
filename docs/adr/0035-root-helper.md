# 0035 – Root-Helper: Protokoll, Operationen und Läufe

- Status: accepted
- Datum: 2026-11-24
- Bezug: KONZEPT.md §4.2, §5, §14 (D9, D15); ADR 0016, ADR 0020; IMPLEMENTIERUNGSPLAN M4-01 bis M4-04, M4-12;
  Threat Model A1, TB4 (T38 bis T41)

## Kontext

Der Agent läuft ohne root (D9). Für Updates und Reboots braucht er einen Helper, der als root läuft und nur ein festes
Enum an Operationen annimmt (ADR 0016, KONZEPT §4.2). Ein Fehler im Helper heißt root auf jedem Node (A1); ein
kompromittierter Server darf darüber nicht mehr auslösen, als die Operationen hergeben (T40). Ziel sind
< 1 000 Zeilen.

Kräfte, die das Konzept noch offen ließ:

- **Läufe überleben den Agent.** apt, dnf und apk aktualisieren auch das Paket `sermon-agent`; dessen Skript startet
  den Agent neu (`try-restart`). Ein Lauf darf daran nicht hängen, und sein Ergebnis darf nicht verloren gehen
  (`CommandResult` ist nicht gepuffert).
- **Läufe überleben den Helper.** Aktualisiert ein Lauf das Paket des Helpers, startet dessen Skript den Helper neu.
  Unter systemd beendet das Stoppen einer Unit alle Prozesse ihrer cgroup, also auch ein laufendes `apt-get`.
- **Versionen laufen auseinander.** Das Self-Update (E4.5) ersetzt nur die Binary des Agents. Agent und Helper
  müssen über Versionen hinweg miteinander reden.
- **Fremde Daten.** Die Ausgaben der Paketmanager kommen aus Quellen Dritter. Ein Parser als root vergrößert die
  Angriffsfläche (Review M3, §6.9).

## Optionen

**Format auf dem Socket**

1. **JSON mit serde:** lesbar, aber ohne Prüfung der Kompatibilität; die Felder hielte man von Hand synchron.
2. **Eigenes Binärformat:** keine Abhängigkeit, aber jedes Feld selbst kodiert, mehr Code im Helper.
3. **Protobuf** (`proto/sermon/helper/v1/helper.proto`, prost), Länge vorangestellt: wie alle anderen Grenzen
   (D26); `buf breaking` prüft die Kompatibilität; der generierte Code zählt nicht zum eigenen Code.

**Wo geparst wird**

1. **Im Helper:** Er gäbe dem Agent fertige Paketlisten; der Parser fremder Ausgaben liefe als root.
2. **Im Agent:** Der Helper führt nur feste Befehle aus und gibt die Ausgabe begrenzt zurück; geparst und gefuzzt wird
   ohne Rechte (`sermon-updates`).

**Wie ein Lauf ausgeführt wird**

1. **Als Kindprozess des Helpers:** einfach, stirbt aber mit der Unit des Helpers.
2. **`KillMode=process` für den Helper:** Kinder überleben, aber systemd verliert die Kontrolle über sie (Zeitlimit,
   Aufräumen), und der Modus gilt als veraltet.
3. **Eigene Unit pro Lauf** (`systemd-run`), unter OpenRC ein abgelöster Prozess in eigener Session: Der Lauf hängt
   weder am Agent noch am Helper.

**Woran der Helper den Aufrufer erkennt**

1. **Nur Dateirechte des Sockets** (`root:sermon 0660`).
2. **Zusätzlich `SO_PEERCRED`** mit der UID des Users `sermon`.
3. **Zusätzlich die Binary des Aufrufers** (`/proc/<pid>/exe`): Das ist ein Rennen (die PID kann wechseln) und schützt
   nicht vor einem kompromittierten Agent, der ohnehin die richtige Binary ist.

## Entscheidung

Gewählt: **Protobuf, Parsen im Agent, eine Unit pro Lauf, Dateirechte und `SO_PEERCRED`.**

### Protokoll

- Unix-Socket `/run/sermon/helper.sock`, `root:sermon 0660`. Eine Anfrage pro Verbindung: 4 Byte Länge (Big Endian),
  dann die Nachricht. Anfragen höchstens 64 KiB, Antworten höchstens 8 MiB. Die Anfrage muss in 5 s vollständig sein.
- `proto/sermon/helper/v1/helper.proto` mit `HelperRequest` (`oneof` der Operationen) und `HelperResponse`.
  Generiert wird nur für Rust, in ein eigenes kleines Crate (`sermon-helper-proto`, nur prost), das Agent und Helper
  nutzen. `buf breaking` gilt wie für die anderen Pakete. Eine unbekannte Operation beantwortet der Helper mit
  `UNSUPPORTED`; der Agent meldet dann „Helper zu alt“.
- Antworten tragen einen Status (`OK`, `REFUSED` mit Grund, `BUSY`, `UNSUPPORTED`, `FAILED`) und die Version des
  Helpers.

### Operationen

Das Enum aus KONZEPT §4.2, ergänzt um zwei lesende Operationen. Jede weitere braucht ein eigenes ADR (ADR 0016).

| Operation | Wirkung | Veto |
|---|---|---|
| `GetStatus` | Version des Helpers, Distro-Familie, die Werte aus `helper.toml`, laufende Läufe | – |
| `ListUpdates{refresh}` | optional Quellen aktualisieren (`apt-get update`, `dnf makecache`, `apk update`), dann die Listen und die Hinweise auf einen nötigen Reboot; die Ausgaben roh und je Befehl auf 2 MiB begrenzt | – |
| `ApplyUpdates{run_id, scope}` | startet einen Lauf (`security` oder `all`, KONZEPT §14.2) und antwortet sofort | `allow_remote_updates` |
| `Reboot{run_id, reason}` | Reboot nach einer Minute Vorlauf, mit Grund | `allow_remote_reboot` |
| `SelfUpdate{run_id, …}` | ersetzt die Binary des Agents; Felder und Ablauf in ADR 0036 | `allow_self_update` |
| `RunResult{run_id}` | Zustand eines Laufs (läuft, fertig), Exit-Code, Dauer, die letzten 256 KiB der Ausgabe | – |

- `run_id` ist eine UUID und sonst nichts: Sie steht in Dateinamen und Unit-Namen.
- `scope` ist ein Enum; Paketnamen, Argumente oder Pfade nimmt keine Operation an. Alpine kennt nur `all` (O6), ein
  `security` lehnt der Helper dort ab.
- Höchstens ein schreibender Lauf zur Zeit (Sperrdatei mit `flock`, die auch der Lauf selbst hält); `ListUpdates` mit
  `refresh` nimmt dieselbe Sperre. Sonst `BUSY`.
- `refresh` höchstens alle 5 min, gezählt im Helper: Ein kompromittierter Agent soll Host und Spiegel nicht mit
  `apt-get update` fluten. Öfter liefert der Helper die Listen ohne Aktualisierung.

### Ausführung

- Die Distro liest der Helper selbst aus `/etc/os-release` (`ID`, `ID_LIKE`): apt, dnf oder apk; sonst `UNSUPPORTED`.
- Befehle mit absoluten Pfaden und fester Umgebung: `PATH=/usr/sbin:/usr/bin:/sbin:/bin`, `LC_ALL=C`, `stdin` aus
  `/dev/null`, `umask 022`, nicht interaktiv (`DEBIAN_FRONTEND=noninteractive`, `NEEDRESTART_MODE=l` u. ä.; Liste in
  M4-03). Zeitlimits: Liste 10 min, Updates 60 min.
- **Ein Lauf ist eine eigene Unit:** Der Helper legt die Anfrage root-eigen unter
  `/var/lib/sermon-helper/runs/<run_id>/` ab und startet
  `systemd-run --unit=sermon-helper-run-<run_id> --collect /usr/bin/sermon-helper run <run_id>`. Unter OpenRC startet
  er denselben Befehl abgelöst in einer eigenen Session (`setsid`). Der Lauf schreibt sein Ergebnis in dasselbe
  Verzeichnis; nach 7 Tagen wird es gelöscht.
- Der Agent fragt das Ergebnis mit `RunResult` ab, auch nach seinem eigenen Neustart (M4-12).

### Aufrufer und lokale Hoheit

- Der Socket hat die Rechte `root:sermon 0660`. Zusätzlich prüft `SO_PEERCRED` die UID des Users `sermon`: aus
  `/etc/passwd` beim Start gelesen, nie aus der Umgebung. Andere UIDs, auch root, werden abgewiesen und protokolliert.
- `/etc/sermon/helper.toml` gehört root und ist nicht für Gruppe oder andere beschreibbar, sonst startet der Helper
  nicht. Der Server kann die Werte nicht ändern. Ausgeliefert wird:

  ```toml
  allow_remote_updates = true   # Pakete nur aus den signierten Quellen der Distro
  allow_remote_reboot = false   # ein Reboot der ganzen Flotte wäre der größte Schaden eines kompromittierten Servers
  allow_self_update = true      # nur Binaries mit der Signatur des Release-Keys (ADR 0036)
  ```

  `install.sh` setzt die Werte mit `--no-remote-updates`, `--allow-remote-reboot` und `--no-self-update`. Wer gar
  keine Fernsteuerung will, installiert den Helper nicht (`--no-helper`, eigenes Paket `sermon-helper`).

### Start und Paket

- **systemd:** `sermon-helper.socket` (Socket-Aktivierung, Rechte wie oben) und `sermon-helper.service`. Der Helper
  beendet sich nach 60 s ohne Anfrage; laufende Läufe sind eigene Units und bleiben.
- **OpenRC:** ein Dienst unter `supervise-daemon`, der den Socket selbst anlegt.
- **Eigenes Paket** `sermon-helper` (`.deb`, `.rpm`, `.apk`), das Paket des Agents empfiehlt es. `install.sh`
  installiert beides.
- **Härtung der Unit** nur, wo sie die Paketmanager nicht bricht. Die Läufe brauchen volles root (Skripte der Pakete
  schreiben überall, Kernel-Pakete bauen die initramfs). Der Score von `systemd-analyze security` wird dokumentiert,
  nicht als Ziel gesetzt. Der Schutz ist die kleine Fläche: feste Operationen, keine freien Argumente.

### Audit

- Jede Anfrage schreibt eine Zeile nach journald (unter OpenRC ins Log des Dienstes): Operation, `run_id`, Ergebnis,
  bei Ablehnung der Grund. Das ist der Nachweis, den ein kompromittierter Agent nicht fälschen kann.
- Der Agent meldet Ergebnisse und Ablehnungen an den Server (`CommandResult`), der sie ins Audit-Log schreibt (T41).
  Die Ausgabe eines Laufs redigiert der Agent vor dem Senden.

### Umfang

`just helper-loc` zählt den eigenen Code des Helpers ohne Tests und generierten Code. Über 1 000 Zeilen schlägt CI
fehl.

## Konsequenzen

- Positiv:
  - Ein Lauf übersteht Neustarts von Agent und Helper, und sein Ergebnis bleibt abholbar.
  - Der Helper parst keine fremden Ausgaben; die Parser laufen ohne Rechte und werden gefuzzt (M4-06).
  - Unterschiedliche Versionen von Agent und Helper bleiben über `buf breaking` und `UNSUPPORTED` beherrschbar.
- Negativ / Risiken:
  - `ListUpdates` liefert bis zu einigen MiB roher Ausgabe über den Socket. Das ist selten (alle 6 h) und begrenzt.
  - Unter OpenRC gibt es keine eigene Unit pro Lauf: Zeitlimit und Aufräumen übernimmt der Lauf selbst.
  - Reboot ist ab Werk aus. Für `autoReboot` in einer Policy muss jeder Host ihn freigeben; die UI zeigt das Veto
    (`GetStatus`) an, statt den Lauf scheitern zu lassen.
- Security-Auswirkungen:
  - Ein kompromittierter Agent kann Updates aus den Quellen der Distro, einen Reboot (wenn erlaubt) und ein
    Self-Update auf eine signierte Version auslösen. Mehr nicht: keine Paketnamen, Befehle oder Pfade.
  - Ein kompromittierter Server kann nicht mehr als der Agent (T40).
  - Andere lokale User erreichen den Helper nicht (T39).
- Folgearbeiten: M4-02 bis M4-04 (Helper und Paket), M4-05 (Server-Protokoll), M4-12 (Agent), ADR 0036
  (`SelfUpdate`); KONZEPT §4.2 und §5 nachgezogen.
