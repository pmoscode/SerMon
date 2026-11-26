# Reallocated sectors

ATA attribute 5 (SCSI: grown defects): sectors the disk replaced with spares. A rising value means the disk wears out.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_reallocated_sectors`
