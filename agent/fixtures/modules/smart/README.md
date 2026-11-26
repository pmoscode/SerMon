# SMART-Fixtures (M5-18)

Ausgaben von `smartctl --json=c -i -H -A -n standby /dev/…`, wie sie der Root-Helper liefert, je Version von
smartmontools der Zieldistros: **7.2** (Rocky/RHEL 9), **7.4** (Debian 13, Ubuntu 24.04), **7.5** (Fedora 44,
Alpine 3.22).

| Datei | Herkunft |
|---|---|
| `*/open-failed.json` | echt: Container `rockylinux:9`, `debian:13`, `alpine:3.22` ohne Disk (`open device … failed`) |
| `*/nvme.json`, `*/nvme-worn.json` | nach einer echten Ausgabe von smartctl 7.5 (NVMe); Gerät, Modell, Seriennummer und Werte ersetzt, Felder je Version wie in deren Quelltext (`temperature_sensors` erst ab 7.3, `spare_available`/`endurance_used` ab 7.5) |
| `*/sata-ssd.json`, `7.4/hdd*.json`, `*/sas.json` | nach dem Format von `ataprint.cpp`/`scsiprint.cpp` der Version, Seriennummern erfunden |
| `*/standby.json` | `-n standby` bei einer schlafenden Disk: nur `device` und die Meldung `Device is in STANDBY mode, exit(2)` |

Ein Fall je Zustand: gesund (`sata-ssd`, `hdd`, `nvme`, `sas`), fehlerhaft (`hdd-failing`: `smart_status.passed`
false, wieder zugewiesene und schwebende Sektoren; `nvme-worn`: kritische Warnung, Reserve unter der Schwelle,
Abnutzung über 100 %), schlafend und nicht lesbar.
