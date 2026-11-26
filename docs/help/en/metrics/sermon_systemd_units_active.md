# Unit active

1 if the unit is active (`ActiveState=active`), else 0. A timer is active while it waits; a service in a restart loop is mostly not active.

## Reading it

From the module `systemd_units`; the label `unit` is the configured unit.

Metric: `sermon_systemd_units_active`
