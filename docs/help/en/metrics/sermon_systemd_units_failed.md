# Unit failed

1 if the unit failed (`ActiveState=failed`), else 0. The cause is in the unit's journal (`journalctl -u <unit>`).

## Reading it

From the module `systemd_units`; the label `unit` is the configured unit.

Metric: `sermon_systemd_units_failed`
