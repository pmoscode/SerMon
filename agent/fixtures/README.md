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
