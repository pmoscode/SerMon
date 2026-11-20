# CPU usage by mode

Share of the time of all cores per mode: `user` (programs), `system` (kernel), `iowait` (waiting for disks), `steal` (taken by the hypervisor), `idle` and others. Together the modes add up to 100 %.

## Reading it

A lot of `iowait` points to slow disks, a lot of `steal` to an overbooked virtualization host.

Metric: `sermon_cpu_utilization_ratio`
