# Critical warning (NVMe)

NVMe: bit field of critical warnings; 0 is fine.

## Reading it

Read by the module `smart` through the root helper (`smartctl --json`, read-only); the label `device` is the disk.

Metric: `sermon_smart_critical_warning`
