# Agent-Fixtures

Echte Dateien aus `/proc`, `/sys` und `/etc` je Distribution, für Parser- und Collector-Tests. Ein Verzeichnis ist
eine Wurzel für `ProcRoot` (`sermon-core`): `<distro>/proc/…`, `<distro>/sys/…`, `<distro>/etc/…`.

| Verzeichnis | Quelle |
|---|---|
| `debian-13/` | Container `debian:13` (amd64) |
| `ubuntu-24.04/` | Container `ubuntu:24.04` (amd64) |

## Logs (`logs/`)

Ausgabe von `journalctl -o json --all` aus Containern mit systemd als PID 1 (Spike M0-10,
[ADR 0013](../../docs/adr/0013-log-quelle-journald.md), erzeugt mit `spikes/journald/run.sh` auf arm64), unverändert
bis auf ersetzte Werte.

| Datei | Inhalt |
|---|---|
| `logs/<distro>/journal-security.jsonl` | sshd (falsches und richtiges Passwort, unbekannter User), su, sudo (Erfolg, drei Fehlversuche), useradd/usermod/userdel |
| `logs/<distro>/journal-format.jsonl` | Sonderfälle: binärer `MESSAGE` (Byte-Array; bei Rocky 8 rohe Bytes, also **ungültiges UTF-8 im JSON**), Feld mit 5 000 Byte, Priorität per syslog |

Distributionen: `debian-13` (systemd 257), `ubuntu-24.04` (255), `rockylinux-9` (252), `rockylinux-8` (239). Ab
Debian 13 heißt der SSH-Absender `sshd-session`. `_CMDLINE` von `su` enthielt das Test-Passwort und zeigt jetzt
`fixture-password` – ein Fall für die Redaction (KONZEPT §10.3).

Hinweise:

- Dateien unter `proc/` stammen aus dem Container und zeigen damit den Kernel der Docker-VM (`linuxkit`). Für die
  Parser ist das Format entscheidend, nicht der Kernel; Fixtures von echten Hosts kommen mit den Collectors (M1-16 ff.).
- Hostname und `machine-id` sind ausgedacht (in den Logs `fixture-host` und `0123456789abcdef0123456789abcdef`). Keine echten Hostnamen, IPs, Usernamen oder IDs einchecken
  (Beispiel-IPs: 192.0.2.0/24, 2001:db8::/32; User: `alice`, `bob`).
