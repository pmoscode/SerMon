# SMART

Health, temperature and error counters of the disks, read with `smartctl` through the root helper. The module is off
by default and reads at most every 10 minutes (adjustable); it doesn't wake a sleeping disk.

## Requirements

- The root helper is installed (`install.sh` without `--no-helper`, package `sermon-helper`) and allows it:
  `allow_smart = true` in `/etc/sermon/helper.toml` (on by default, `install.sh --no-smart` switches it off).
- The package `smartmontools` is installed (`/usr/sbin/smartctl`).

The helper chooses the disks itself: whole SATA, SAS and USB disks (`sda` …) and NVMe namespaces (`nvme0n1` …), at
most 24. `smartctl` only asks for identity, health and attributes; it writes nothing and starts no self-test. Virtual
disks (`vda`, `xvda`) have no SMART and don't appear.

## Settings

- **Excluded disks:** e.g. `sdb` for a USB stick without SMART.

## Metrics

- `sermon_smart_passed`: the disk's self-assessment, 1 passed, 0 **failed** – the disk expects to fail; back up the
  data and replace it.
- `sermon_smart_temperature_celsius`: the temperature.
- `sermon_smart_reallocated_sectors`, `sermon_smart_pending_sectors`, `sermon_smart_uncorrectable_sectors` (SATA):
  replaced, pending and unreadable sectors. If they grow, the disk is ageing.
- `sermon_smart_crc_errors` (SATA): transfer errors, usually cable or connector.
- `sermon_smart_media_errors`, `sermon_smart_critical_warning`, `sermon_smart_spare_ratio`,
  `sermon_smart_endurance_used_ratio` (NVMe): data errors, critical warning (0 is fine), remaining spare and wear
  (1 = the amount of writes the vendor promises is reached).
- `sermon_smart_power_on_seconds`, `sermon_smart_power_cycles`: power-on time and power cycles.
- `sermon_smart_readable` and `sermon_smart_standby`: whether data came; a sleeping disk gives nothing but
  `standby = 1`.

## If it doesn't run

- "allow_smart = false": the host doesn't allow it (`helper.toml`).
- "install smartmontools": `smartctl` is missing.
- "unknown operation": the root helper is older than the agent; run `install.sh` again or update the package.
- "SMART was read less than a minute ago": the helper reads at most once a minute.
