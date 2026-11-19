# Agent-Fixtures

Echte Dateien aus `/proc`, `/sys` und `/etc` je Distribution, für Parser- und Collector-Tests. Ein Verzeichnis ist
eine Wurzel für `ProcRoot` (`sermon-core`): `<distro>/proc/…`, `<distro>/sys/…`, `<distro>/etc/…`.

| Verzeichnis | Quelle |
|---|---|
| `debian-13/` | Container `debian:13` (amd64) |
| `ubuntu-24.04/` | Container `ubuntu:24.04` (amd64) |

Hinweise:

- Dateien unter `proc/` stammen aus dem Container und zeigen damit den Kernel der Docker-VM (`linuxkit`). Für die
  Parser ist das Format entscheidend, nicht der Kernel; Fixtures von echten Hosts kommen mit den Collectors (M1-16 ff.).
- Hostname und `machine-id` sind ausgedacht. Keine echten Hostnamen, IPs, Usernamen oder IDs einchecken
  (Beispiel-IPs: 192.0.2.0/24, 2001:db8::/32; User: `alice`, `bob`).
