# Power cycles

How often the disk was powered on.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_power_cycles`
