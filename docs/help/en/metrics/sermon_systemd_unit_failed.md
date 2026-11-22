# systemd unit failed

1 for a unit in the state "failed", 0 for one that failed within the last 24 hours and runs again.

## Reading it

Units that never failed do not appear. `systemctl status <unit>` and `journalctl -u <unit>` show the cause. The agent asks systemd over D-Bus; without D-Bus the metric is missing.

Metric: `sermon_systemd_unit_failed`
