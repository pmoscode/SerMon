# SMART readable

1 if the disk's SMART data was read; 0 in standby or if it is not readable.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_readable`
