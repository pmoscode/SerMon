# Uncorrectable sectors

ATA attribute 198: sectors with errors the disk could not correct.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_uncorrectable_sectors`
