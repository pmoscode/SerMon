# Automatic restarts

Services only: how often systemd restarted the service since its last start by hand (`NRestarts`, `Restart=`). If it keeps rising, the service keeps crashing.

## Reading it

From the module `systemd_units`; the label `unit` is the configured unit.

Metric: `sermon_systemd_units_restarts`
