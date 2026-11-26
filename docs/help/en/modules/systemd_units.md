# systemd units

State and automatic restarts of selected systemd units – next to the failed units the agent reports anyway
(`sermon_systemd_unit_failed`). The module is off by default.

## Requirements

A host with systemd. The agent asks `systemctl show` with fixed properties and checked names, without a shell and
without extra rights (systemd answers an unprivileged user over D-Bus). On Alpine (OpenRC) the module reports "this
host runs no systemd".

## Settings

- **Units:** up to 32 names with their type, e.g. `nginx.service`, `postgresql@17-main.service`, `backup.timer`.

## Metrics

- `sermon_systemd_units_loaded`: systemd knows the unit.
- `sermon_systemd_units_active`, `sermon_systemd_units_failed`: active or failed.
- `sermon_systemd_units_restarts`: automatic restarts (`Restart=`); if it keeps rising, the service keeps crashing,
  even if it looks active in between.

## When it doesn't work

- `sermon_systemd_units_loaded` 0: the name is wrong or the service isn't installed.
- An alias (e.g. `dbus.service` on Rocky Linux) counts under the configured name.
