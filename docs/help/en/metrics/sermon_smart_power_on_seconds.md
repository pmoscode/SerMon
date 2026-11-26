# Disk power-on time

How long the disk has run in total.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_power_on_seconds`
