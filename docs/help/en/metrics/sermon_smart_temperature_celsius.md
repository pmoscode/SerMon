# Disk temperature

The disk's current temperature.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_temperature_celsius`
