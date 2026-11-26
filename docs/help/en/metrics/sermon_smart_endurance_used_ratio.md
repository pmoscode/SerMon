# Endurance used (NVMe)

NVMe: endurance used as the vendor rates it; may pass 100 %.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_endurance_used_ratio`
