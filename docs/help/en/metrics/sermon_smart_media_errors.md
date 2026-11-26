# Media errors (NVMe)

NVMe: media and data integrity errors.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_media_errors`
