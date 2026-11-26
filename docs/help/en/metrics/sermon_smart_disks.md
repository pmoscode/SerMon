# Disks read

How many disks the root helper read with `smartctl`.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_disks`
