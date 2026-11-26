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
3. **Eigene Unit pro Lauf** (`systemd-run`), unter OpenRC ein abgelöster Prozess in eigener Prozessgruppe: Der Lauf hängt
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
  er denselben Befehl abgelöst in einer eigenen Prozessgruppe (`supervise-daemon` signalisiert nur den Helper). Der Lauf schreibt sein Ergebnis in dasselbe
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

`just helper-loc` zählt den eigenen Code des Helpers ohne Tests und generierten Code. Über 1 000 Zeilen (seit dem
Nachtrag M4-03: 1 500) schlägt CI
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

## Nachtrag M4-02

- Der Helper bedient Verbindungen nacheinander: Der Agent ist der einzige Aufrufer, und ein schreibender Lauf hält
  ohnehin die Sperre.
- Fremde Aufrufer und Verbindungen ohne Credentials schließt er, ohne die Anfrage zu lesen.
- Eine unsichere, ungültige `helper.toml` (unbekannter Schlüssel, falscher Typ) oder ein fehlendes Verzeichnis
  `/etc/sermon` verhindern den Start: lieber keine Fernsteuerung als eine mit falschen Rechten. Ohne Datei gelten die
  Werte oben.
- Ein selbst gebundener Socket entsteht unter einer umask (nie offener als `0660`), das Verzeichnis `/run/sermon` mit
  `0750` und der Gruppe `sermon`.
- Die einzige `unsafe`-Stelle übernimmt fd 3 von systemd, nachdem `LISTEN_PID` und `LISTEN_FDS` geprüft sind; fd 3
  muss ein Socket sein.
- Das Format des Journals: `peer_uid=… op=… run_id=… status=… reason="…"`; eine `run_id` steht nur geprüft darin.

## Nachtrag M4-03: Ausführung, Grenze 1 500 Zeilen

- **Grenze:** Mit den Paketoperationen hat der Helper 1 134 eigene Zeilen (im Stil von rustfmt, ohne Tests und
  Kommentare); das Self-Update kommt noch hinzu. Die Grenze steigt auf **1 500 Zeilen** und bleibt in CI geprüft
  (`scripts/helper-loc.sh`). Gezählt wird bis zum Testmodul; ein `#[cfg(test)]` an einem Element im Code zählt mit.
  Gegen das Ziel „winzig“ spricht das nicht: Die Fläche bleibt das feste Op-Enum ohne freie Argumente.
- **Befehle** (absolute Pfade, feste Umgebung, eigene Prozessgruppe; Listen 10 min mit 2 MiB nur aus stdout, Läufe
  60 min mit den letzten 256 KiB aus stdout und stderr):

  | | Quellen | Listen | alle | Security |
  |---|---|---|---|---|
  | apt | `apt-get update -q` | `apt list --upgradable` | `apt-get -y -q -o …confdef -o …confold --with-new-pkgs upgrade` | `apt-get … install --only-upgrade NAMEN` |
  | dnf | `dnf -q makecache --refresh` (Nachtrag M4-20) | `check-update`, `updateinfo list --security`, `needs-restarting -r` | `dnf -y upgrade` | `dnf -y upgrade --security` |
  | apk | `apk update -q --force-refresh` (Nachtrag M4-20) | `apk version -l '<'` | `apk upgrade -q` | – (`UNSUPPORTED`, O6) |

  Ein Lauf aktualisiert erst die Quellen; schlägt das fehl, endet er mit dessen Ausgabe.
- **Abweichung: apt und „nur Security“.** apt kennt kein Upgrade nur aus Security-Quellen. Der Helper liest dafür doch
  eine Ausgabe: aus `apt list --upgradable` die Namen der Pakete, deren Suite auf `-security` endet. Er übernimmt nur
  Namen nach den Regeln von Debian (Kleinbuchstaben, Ziffern, `+-.`, ab zwei Zeichen); sie werden zu Argumenten von
  `apt-get`, nie zu einer Shell-Zeile.
- **Reboot:** unter systemd `shutdown -r +1 GRUND`, unter OpenRC `reboot -d 60`. busybox kehrt nicht zurück: Das
  Ergebnis wird dort vorher geschrieben.
- **Läufe:** `/var/lib/sermon-helper/runs/<run_id>/` (nur root) mit `request`, `started` und `result` (`RunState`
  als Protobuf, atomar geschrieben); nach 7 Tagen gelöscht. Unter systemd startet `systemd-run` die Unit
  `sermon-helper-run-<run_id>`, sonst ein eigener Prozess in eigener Prozessgruppe; beide führen
  `sermon-helper run <run_id>` aus. Eine schon bekannte `run_id` startet nichts mehr. Ein Lauf ohne Ergebnis,
  dessen Sperre niemand hält, gilt als verloren (Exit -1).
- **Sperre:** `/var/lib/sermon-helper/lock` (`flock`), darin die `run_id` des laufenden Laufs für `GetStatus`. Ein
  Start prüft sie; zwei Starts kurz nacheinander kann nur der Lauf selbst trennen: Der zweite endet dann mit
  „another run holds the lock“, es laufen nie zwei Paketmanager.
- **Ausgabe von Daemons:** Startet das Skript eines Pakets einen Dienst, der die Ausgabe offen hält, wartet der Lauf
  nach dem Ende des Befehls noch 2 s auf die Ausgabe und endet dann.

## Nachtrag M4-04: Paket, install.sh, Review vor der Nutzung

- **Paket `sermon-helper`** (`.deb`, `.rpm`, `.apk`): Binary, `sermon-helper.socket` und `.service` bzw. das
  OpenRC-Skript, `helper.toml` als Konfiguration (`root:root 0644`, bleibt bei Updates). Es hängt vom Agent ab, der
  Agent empfiehlt es. Die Paketskripte aktivieren Socket bzw. Dienst nur bei der Erstinstallation; ein Update startet
  einen laufenden Helper neu, aktiviert aber keinen, den der Admin abgeschaltet hat. Das `%pre` legt den User `sermon`
  wie das Agent-Paket an: rpm führte in der Demo das `%post` des Helpers vor dem `%pre` des Agents aus, und der Socket
  braucht die Gruppe.
- **`install.sh`** lädt und prüft die signierte Binary wie die des Agents (fehlt sie beim Server, läuft der Agent ohne
  Helper weiter, eine falsche Signatur bricht ab). Schalter `--no-remote-updates`, `--allow-remote-reboot`,
  `--no-self-update` ändern nur ihren Wert in `helper.toml`; ohne Schalter bleibt eine vorhandene Datei unverändert.
  Nur die Erstinstallation aktiviert den Helper; `--no-helper` schaltet einen vorhandenen ab.
- **`/run/sermon`** ist unter systemd `0755 root:root` (`DirectoryMode`), unter OpenRC `0750 root:sermon`; maßgeblich
  ist der Socket mit `0660 root:sermon`.
- **Unit:** `systemd-analyze security` 5,8 (in `just test-packaging` höchstens 6,0); `ProtectSystem=full`,
  `ProtectHome`, `NoNewPrivileges`, Namensräume, `UMask=0022`, `Nice=10`, IO-Klasse `idle`, `CPUQuota=50%`. Die Läufe
  sind eigene Units und daran nicht gebunden.
- **Review vor der Nutzung** (ohne Kontext der Umsetzung): kein kritischer Befund; behoben:
  - **Hoch (mit erlaubtem Reboot):** Der Grund ging als Argument an `shutdown`, `-h` hätte die Flotte ausgeschaltet.
    Jetzt `shutdown -r -- +1 "SerMon: GRUND"`.
  - **Mittel:** Läufe in Folge umgingen die Grenze fürs Aktualisieren und füllten das State-Verzeichnis: Je Art
    (Updates, Reboot) startet höchstens ein Lauf in 5 min (sonst `BUSY` mit Grund), höchstens 500 Läufe bleiben.
    `install.sh` respektierte `--no-helper` und einen abgeschalteten Helper nicht.
  - **Niedrig:**
    - Der Socket von systemd (fd 3) vererbte sich an apt, dnf und `systemd-run`: jetzt `FD_CLOEXEC`, und fd 3 muss
      ein lauschender Unix-Stream-Socket sein.
    - Die Ersatzantwort für zu Großes war selbst zu groß.
    - Das Journal ließ sich fluten: höchstens 20 Anfragen je Sekunde; Läufe schreiben Start und Ende; unter OpenRC
      loggt der Helper über syslog, das rotiert.
    - Listen ohne Grenze an CPU: `Nice`, IO-Klasse, `CPUQuota`.
  - **Hinweise:** Eine bekannte `run_id` für einen anderen Lauf wird abgelehnt; ein Lauf wartet bis zu 5 s auf die
    Sperre (statt an einem kurzen `GetStatus` zu scheitern), Listen ebenso; Security-Updates mit `apt-get --no-remove`.

## Nachtrag M4-12: Befehle im Agent

- Die Session reicht `RunUpdate`, `RebootNode` und `SelfUpdate` an den Läufer des Agents weiter (Kanal mit 64
  Plätzen, die Session wartet nie). Er startet den Lauf beim Helper (`ApplyUpdates`, `Reboot`, `SelfUpdate`), meldet
  `STARTED` und fragt alle 10 s mit `RunResult` nach dem Ende; das Ergebnis (`SUCCEEDED` bei Exit-Code 0, sonst
  `FAILED`, `LOST`, wenn der Helper den Lauf nicht mehr kennt) geht gepuffert an den Server.
- **Einmal je `run_id`:** Das Laufbuch (`/var/lib/sermon-agent/runs`, Zeilen `run_id Zustand Zeit`) kennt jeden
  genommenen Lauf. Ein erneut zugestellter Befehl startet nichts. Nach einem Neustart des Agents (etwa durch sein
  eigenes Paket-Update) fragt er die laufenden weiter ab. Fällt der Agent zwischen Start und Eintrag aus, stellt der
  Server den Befehl beim nächsten Reconnect erneut zu; der Helper nimmt dieselbe `run_id` mit derselben Anfrage als
  schon gestartet an. Das Buch hält höchstens 500 Läufe, beendete höchstens 8 Tage.
- **Veto und Fehler:** Ein abgelehnter Start (`helper.toml`) wird `REFUSED`, kein oder ein zu alter Helper
  `UNSUPPORTED`, jeweils mit Grund. Ein beschäftigter Helper wird alle 30 s erneut gefragt, nach 15 min – oder früher
  zur Frist des Befehls, `valid_for_seconds`, M4-31 – wird der Lauf `FAILED`.
- **Ausgabe:** Vor dem Senden läuft sie durch die Redaction der Org (wie Logs und Kommandozeilen) und wird auf ihr Ende
  von höchstens 256 KiB gekürzt.

## Nachtrag M4-17

- Ablauf und Felder des Self-Updates regelt [ADR 0036](0036-self-update-und-paket-repos.md): `SelfUpdate` bekommt das
  Feld `version`, neu ist `ConfirmSelfUpdate{run_id}`. Die Grenze des Helpers steigt dafür von 1 500 auf
  **1 800 Zeilen**; Ed25519 kommt aus `ring` (schon im Workspace, Nachtrag M4-18 zu ADR 0036).

## Nachtrag M4-20

- **dnf und apk frischen die Quellen wirklich auf:** `dnf makecache` lädt nichts, solange die Metadaten jünger als
  `metadata_expire` sind (Standard 48 h), `apk update` nichts, solange sein Cache jünger als `cache-max-age` ist (4 h,
  gefunden in der Demo: das neue Paket im Repo des Servers kam auf Alpine nicht an). Nur `apt-get update` lädt immer.
  Der Helper ruft daher `dnf -q makecache --refresh` und `apk update -q --force-refresh`; er frischt höchstens alle
  5 min auf (T89), die Last bleibt begrenzt.


## Nachtrag M4-30 (Security-Review)

- **Veto bei jeder Anfrage:** `helper.toml` wird nicht mehr nur beim Start gelesen, sondern für jede Anfrage; ist die
  Datei unlesbar oder unsicher, lehnt der Helper jede Anfrage ab (fail closed). Ein angenommener Lauf prüft das Veto
  bei seinem Start erneut und endet sonst mit dem Grund. Eine Änderung gilt damit ab der nächsten Anfrage, auch wenn
  der Agent den per Socket gestarteten Helper durch seine Abfragen wach hält.
- **Journal:** Die Zeile jeder Anfrage nennt die PID des Aufrufers und die Parameter (Scope, Version, Grund – maskiert
  und auf 200 Bytes gekürzt).
- **Grund eines Reboots:** abgelehnt werden alle Format- und Trennzeichen (Cf, Zl, Zp) und die Hangul-Füllzeichen,
  wie im Server (`textsafe`) und im Schema.
- **Umfang:** 1 628 eigene Zeilen (Grenze 1 800, ADR 0036).

## Nachtrag M4-31 (Fristen der Befehle)

- Jeder Lauf kommt mit `valid_for_seconds`: der Zeit bis zum Ablauf des Befehls beim Server, für einen Rollout höchstens
  bis zum Ende des Fensters. Als Dauer ab Ankunft, damit die Uhr des Hosts nichts zählt. Später startet der Agent nichts
  mehr – auch nicht nach einem langsamen Download des Releases eines Self-Updates – und meldet `FAILED` mit Grund.
- Der Server schickt keinen Lauf mit weniger als einer Sekunde Restzeit. Ein gesendeter Befehl gilt erst 15 min nach
  seinem Ablauf ohne Antwort als abgelaufen (`SentGrace`): Bis dahin hat der Agent einen rechtzeitigen Start oder den
  Ablauf gemeldet, gepuffert auch über eine Unterbrechung. Der Rollout nennt dann „no answer from the node in time“
  statt „didn't take the command in time“.

## Nachtrag M4-31 (Abbrechen)

- `CancelRun{run_id}` zieht einen gesendeten Lauf zurück. Wartet er noch auf einen beschäftigten Helper, verwirft der
  Agent ihn und meldet `REFUSED` („cancelled on the server“), der Server führt ihn dann als abgebrochen. Kennt der Agent
  ihn nicht (etwa nach einem Neustart), trägt er ihn als beendet ins Laufbuch ein: ein später erneut zugestellter
  Befehl startet nichts. Einen begonnenen Lauf bricht niemand ab – ein Paketmanager mittendrin hinterließe ein halbes
  Update –, er läuft zu Ende.
- Der Server schickt den Rückzug einmal je Session und nach jedem Reconnect erneut, bis der Agent antwortet; den Befehl
  selbst stellt er nicht mehr zu. Ein noch nicht gesendeter Lauf wird sofort storniert (Zustand 6).
- Abbrechen und Pausieren nehmen die Zeile der Update-Lease (`FOR UPDATE`): Eine Speicherung des Orchestrators wartet,
  die nächste verwirft, was sie ohne den Abbruch oder während der Pause geplant hatte.

