# Agent-Fixtures

Echte Dateien aus `/proc`, `/sys` und `/etc` je Distribution, für Parser- und Collector-Tests. Ein Verzeichnis ist
eine Wurzel für `ProcRoot` (`sermon-core`): `<distro>/proc/…`, `<distro>/sys/…`, `<distro>/etc/…`.
Die Geräte unter `sys/block/` sind leere Verzeichnisse; Git versioniert sie nur mit einer leeren `.keep`-Datei (ohne
sie hält der Disk-Collector im frischen Checkout Partitionen für ganze Geräte).

| Verzeichnis | Quelle |
|---|---|
| `debian-13/` | Container `debian:13` (amd64) |
| `ubuntu-24.04/` | Container `ubuntu:24.04` (amd64) |
| `rockylinux-9/`, `almalinux-10/`, `fedora-44/` | M2-19, `capture-host.sh` (arm64): Container `rockylinux:9`, `almalinux:10`, `fedora:44` |

`capture-host.sh image name …` holt dieselben Dateien aus einem Container und ersetzt Hostname und `machine-id`.

## Prozesse (`processes/`)

`processes/capture.sh` (M3-13) holt `/proc/<pid>/{stat,status,cmdline,cgroup}` einiger Sonderfälle aus einem
`debian:13`-Container: ein normaler Prozess, einer von `nobody`, ein Zombie und eine ausführbare Datei namens
`evil) name (x`. Aus dem PID-Namensraum des Docker-Hosts kommen `kthreadd`, ein `kworker` und `containerd` mit vielen
Threads; sie liegen unter PID 9000 + n, weil ihre eigenen PIDs mit denen des Containers kollidieren. Dazu kommen die
Dateien der Kopfzeile und `/etc/passwd`; `pids.txt` nennt die Zuordnung.

## Logs (`logs/`)

Ausgabe von `journalctl -o json --all` aus Containern mit systemd als PID 1 (Spike M0-10,
[ADR 0013](../../docs/adr/0013-log-quelle-journald.md), erzeugt mit `spikes/journald/run.sh` auf arm64), unverändert
bis auf ersetzte Werte.

| Datei | Inhalt |
|---|---|
| `logs/<distro>/journal-security.jsonl` | sshd (falsches und richtiges Passwort, unbekannter User), su, sudo (Erfolg, drei Fehlversuche), useradd/usermod/userdel |
| `logs/<distro>/journal-format.jsonl` | Sonderfälle: binärer `MESSAGE` (Byte-Array; bei Rocky 8 rohe Bytes, also **ungültiges UTF-8 im JSON**), Feld mit 5 000 Byte, Priorität per syslog |
| `logs/<distro>/journal-security-variants.jsonl` | M2-06, `logs/capture/capture.sh`: SSH über IPv6, Root-Login, Public Key (angenommen und unbekannt), keyboard-interactive, User-Namen mit falschem `from … port`, Umlauten und leer; sudo mit Terminal und ohne Recht; **gefälschte Zeilen** eines Users ohne Rechte (`logger -t sshd`, `-t sudo`, `-t useradd`) |
| `logs/<distro>/journal-oom.jsonl` | M2-06: OOM-Kill einer Unit mit `MemoryMax` – die Kernel-Zeilen dazu (Kernel der Docker-VM) und die Meldung von systemd |

Die Varianten erzeugt `logs/capture/capture.sh [image…]` neu (Docker, Container mit systemd als PID 1); das Skript
gehört zum Projekt, anders als der Spike. Distributionen: `debian-13` (systemd 257), `ubuntu-24.04` (255),
`rockylinux-9` (252), `rockylinux-8` (239), seit M2-19 `almalinux-10` und `fedora-44` (nur Varianten und OOM; die
Spike-Dateien gibt es für sie nicht). Fedora bringt `script(1)` im eigenen Paket `util-linux-script` und kein
`hostname(1)` mit; das Capture-Image und `capture.py` berücksichtigen das. Ab
Debian 13 heißt der SSH-Absender `sshd-session`. `_CMDLINE` von `su` enthielt das Test-Passwort und zeigt jetzt
`fixture-password` – ein Fall für die Redaction (KONZEPT §10.3).

**Zustände** (M2-22) unter `states/`: `states/raid/proc/mdstat` ist von Hand im Format von `drivers/md/md.c`
geschrieben (Container haben kein md-RAID): raid1 gesund, raid5 degradiert mit Wiederherstellung, raid0, ein inaktives
und ein raid6 im Resync. `states/systemd/failed-<distro>.txt` ist die echte Ausgabe von
`systemctl list-units --state=failed --plain --no-legend --no-pager --full` in den E2E-Images (Debian 13, Ubuntu
24.04, Rocky 9, Fedora 44) mit zwei absichtlich fehlgeschlagenen Units (`systemd-run --unit=… false`, eine mit
escaptem Namen `app@web\x2d01`).

**Alpine ohne journald** (M2-20, ADR 0025): `logs/alpine-3.22/messages` (busybox `syslogd`) und
`logs/alpine-3.22/security.log` (rsyslog mit der SerMon-Vorlage) erzeugt `logs/capture/capture-alpine.sh`: echte
sshd-, sudo- und useradd-Zeilen und von alice mit `logger -p auth.info` gefälschte. Das sudo von Alpine nutzt kein PAM
(nur `1 incorrect password attempt`). Der Container heißt `fixture-host`.

Hinweise:

- Dateien unter `proc/` und `sys/` stammen aus dem Container und zeigen damit den Kernel der Docker-VM (`linuxkit`) –
  bei allen Distros dieselbe VM (16 Kerne, 8 GiB). Je Distro unterscheiden sich `/etc/os-release` und das Layout. Für die
  Parser ist das Format entscheidend, nicht der Kernel; Fixtures von echten Hosts kommen mit den Collectors (M1-16 ff.).
- Hostname und `machine-id` sind ausgedacht (in den Logs `fixture-host` und `0123456789abcdef0123456789abcdef`). Keine echten Hostnamen, IPs, Usernamen oder IDs einchecken
  (Beispiel-IPs: 192.0.2.0/24, 2001:db8::/32; User: `alice`, `bob`).

## Discovery (`discovery/`)

`discovery/capture.sh` (M5-04) startet die E2E-Images mit systemd (`deploy/e2e/agent.Dockerfile`; Debian 13, Ubuntu
24.04, Rocky 9, Alma 10, Fedora 44) und installiert nginx und sshd aus den Paketquellen der Distribution; Alpine 3.22
startet nginx ohne OpenRC. Erfasst werden `proc/net/{tcp,tcp6,unix}`, `proc/<pid>/comm` aller Prozesse und
`units.txt` (`systemctl list-units --type=service --state=running --plain --no-legend --no-pager --full`, nicht bei
Alpine). Die Verbindungen zu den Paket-Mirrors (`TIME_WAIT`) bleiben drin, ihre Adressen ersetzt das Skript durch
192.0.2.0/24 bzw. 2001:db8::/32. Erfasst auf arm64 am 2026-11-26.

## Module (`modules/`)

`modules/tls/generate.sh` (M5-09) schreibt mit OpenSSL ≥ 3.4 die Zertifikate unter `modules/tls/certs/`: eine Root- und
eine Zwischen-CA, ein Zertifikat für `localhost` und `127.0.0.1` als Kette und allein, ein abgelaufenes, eines für
`other.example` und ein selbst signiertes, mit festen Daten (2025 bis 2045). Die Schlüssel verwirft das Skript.

`modules/systemd_units/capture.sh` (M5-10) nimmt `systemctl show` mit den festen Eigenschaften des Moduls in den
E2E-Images mit systemd auf (Debian 13, Ubuntu 24.04, Rocky 9, Alma 10, Fedora 44): ein laufender Dienst, eine
Neustart-Schleife (`Restart=always`), ein fehlgeschlagener Dienst, eine Template-Instanz, ein Timer und eine unbekannte
Unit. Auf Rocky und Alma ist `dbus.service` ein Alias von `dbus-broker.service`.

`modules/nginx/capture.sh` (M5-11) holt die `stub_status`-Seite aus den offiziellen Images `nginx:<version>-alpine`
(1.22 bis 1.29), nach zehn Anfragen und mit je einer Verbindung, die liest, schreibt und wartet.

`modules/apache/capture.sh` (M5-12) holt `server-status?auto` aus den Apache-Paketen von Debian 13 (MPM event und
prefork), Rocky 9, Alma 10 und Fedora 44 (dort mit eigener `<Location>`), nach zehn Anfragen; der Hostname der ersten
Zeile ist ersetzt.

## Updates (`updates/`)

`updates/capture.sh` (M4-06) führt die Listen-Befehle des Root-Helpers (ADR 0035, Nachtrag M4-03) mit `LC_ALL=C` in
frischen Containern der gepinnten Basis-Images aus, gegen die echten Paketquellen der Distribution (die Images sind
älter, also warten Updates); `<datei>.exit` hält den Exit-Code. Erfasst auf arm64 am 2026-11-24:

| Verzeichnis | Inhalt |
|---|---|
| `debian-12/`, `debian-13/`, `ubuntu-24.04/` | `apt list --upgradable` (Debian 12 ohne Updates) |
| `rockylinux-9/`, `almalinux-10/`, `fedora-44/` | `dnf -q check-update` (100), `dnf -q updateinfo list --security` (dnf 4 und 5), `dnf needs-restarting -r` (auf Rocky ohne `dnf-utils`: Exit 1 ohne Ausgabe) |
| `alpine-3.22/` | `apk version -l '<'` |
| `synthetic/` | **von Hand:** `needs-restarting` mit nötigem Reboot (der Wortlaut von dnf) und eine `apt list`-Ausgabe einer feindlichen Quelle (Steuerzeichen, überlange Namen, kaputte Zeilen) |
