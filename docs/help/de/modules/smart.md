# SMART

Gesundheit, Temperatur und Fehlerzähler der Disks, gelesen mit `smartctl` über den Root-Helper. Das Modul ist ab Werk
aus und liest höchstens alle 10 Minuten (einstellbar); eine schlafende Disk weckt es nicht.

## Voraussetzungen

- Der Root-Helper ist installiert (`install.sh` ohne `--no-helper`, Paket `sermon-helper`) und erlaubt es:
  `allow_smart = true` in `/etc/sermon/helper.toml` (ab Werk an, `install.sh --no-smart` schaltet es ab).
- Das Paket `smartmontools` ist installiert (`/usr/sbin/smartctl`).

Der Helper wählt die Disks selbst: ganze SATA-, SAS- und USB-Disks (`sda` …) und NVMe-Namensräume (`nvme0n1` …),
höchstens 24. `smartctl` fragt nur Identität, Gesundheit und Attribute ab, es schreibt nichts und startet keinen
Selbsttest. Virtuelle Disks (`vda`, `xvda`) haben kein SMART und erscheinen nicht.

## Einstellungen

- **Ausgeschlossene Disks:** etwa `sdb` für einen USB-Stick, der kein SMART kennt.

## Metriken

- `sermon_smart_passed`: die Selbstauskunft der Disk, 1 bestanden, 0 **fehlgeschlagen** – die Disk erwartet ihren
  Ausfall; Daten sichern und tauschen.
- `sermon_smart_temperature_celsius`: die Temperatur.
- `sermon_smart_reallocated_sectors`, `sermon_smart_pending_sectors`, `sermon_smart_uncorrectable_sectors`
  (SATA): ersetzte, schwebende und nicht lesbare Sektoren. Steigen sie, altert die Disk.
- `sermon_smart_crc_errors` (SATA): Übertragungsfehler, meist Kabel oder Stecker.
- `sermon_smart_media_errors`, `sermon_smart_critical_warning`, `sermon_smart_spare_ratio`,
  `sermon_smart_endurance_used_ratio` (NVMe): Datenfehler, kritische Warnung (0 ist gut), verbleibende Reserve und
  Abnutzung (100 % = die vom Hersteller zugesagte Schreibmenge ist erreicht).
- `sermon_smart_power_on_seconds`, `sermon_smart_power_cycles`: Betriebszeit und Einschaltvorgänge.
- `sermon_smart_readable` und `sermon_smart_standby`: ob Daten kamen; eine schlafende Disk liefert nichts außer
  `standby = 1`.

## Wenn es nicht läuft

- „allow_smart = false“: Der Host erlaubt es nicht (`helper.toml`).
- „install smartmontools“: `smartctl` fehlt.
- „unknown operation“: Der Root-Helper ist älter als der Agent; `install.sh` erneut ausführen oder das Paket
  aktualisieren.
- „SMART was read less than a minute ago“: Der Helper liest höchstens einmal je Minute.
