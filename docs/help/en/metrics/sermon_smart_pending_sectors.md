# Pending sectors

ATA attribute 197: sectors that could not be read and wait to be replaced.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_pending_sectors`
