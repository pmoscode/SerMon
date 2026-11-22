# systemd-Unit fehlgeschlagen

1 für eine Unit im Zustand „failed“, 0 für eine, die in den letzten 24 Stunden fehlschlug und wieder läuft.

## Einordnung

Units, die nie fehlschlugen, erscheinen nicht. Die Ursache zeigen `systemctl status <unit>` und `journalctl -u <unit>`. Der Agent fragt systemd über D-Bus; ohne D-Bus fehlt die Metrik.

Metrik: `sermon_systemd_unit_failed`
