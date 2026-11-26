# Disk in standby

1 if the disk sleeps; the agent doesn't wake it.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_standby`
