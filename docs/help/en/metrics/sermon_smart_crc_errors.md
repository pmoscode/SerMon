# CRC errors

ATA attribute 199: transfer errors, mostly cable or connector.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_crc_errors`
