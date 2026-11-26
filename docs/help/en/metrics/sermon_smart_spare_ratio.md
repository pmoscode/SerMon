# Available spare (NVMe)

NVMe: available spare blocks in percent.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_spare_ratio`
