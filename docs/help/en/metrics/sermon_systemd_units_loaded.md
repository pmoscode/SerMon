# Unit loaded

1 if systemd knows the unit (`LoadState=loaded`), else 0 – mostly a typo in the name or a service that isn't installed.

## Reading it

From the module `systemd_units`; the label `unit` is the configured unit.

Metric: `sermon_systemd_units_loaded`
