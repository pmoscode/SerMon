# Spike M0-10: journald über `journalctl`

Wegwerf-Code zur Prüfung, wie der Agent das systemd-Journal über `journalctl` als Kindprozess liest. Ergebnis und
Entscheidung stehen in [ADR 0013](../../docs/adr/0013-log-quelle-journald.md), die Rohausgaben in `results/`. Der Code
gehört nicht zum Produkt, wird nicht gebaut und nicht gepflegt.

Nachstellen (Docker nötig, Container laufen `--privileged` mit systemd als PID 1):

```bash
spikes/journald/run.sh                       # Debian 13, Ubuntu 24.04, Rocky 9, Rocky 8
spikes/journald/run.sh rockylinux:9          # nur eine Distribution
```

- `Dockerfile`: Basis-Image mit systemd, sshd und sudo; User `alice` mit Test-Passwort (nur im Wegwerf-Container).
- `experiments.py`: die Versuche (Format, `--cursor-file`, `--after-cursor`, Rotation, Vacuum, Neustart von journald,
  Latenz, Last, Rechte) und das Erzeugen der Fixtures aus echten sshd-, sudo- und useradd-Ereignissen.
- `results/<distro>.txt`: eine Zeile pro Befund. Die Rohausgaben von `journalctl -o json --all` (`results/<distro>/`)
  liegen als Fixtures unter `agent/fixtures/logs/` und werden hier nicht doppelt eingecheckt.

Hostname, `machine-id` und das Test-Passwort werden in den Fixtures durch `fixture-host`,
`0123456789abcdef0123456789abcdef` und `fixture-password` ersetzt.
