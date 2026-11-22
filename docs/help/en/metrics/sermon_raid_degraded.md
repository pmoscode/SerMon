# RAID degraded

1 when a software RAID (md) is missing at least one disk.

## Reading it

Normal is 0. At 1 the array runs without full redundancy; one more failure can lose data. `cat /proc/mdstat` and `mdadm --detail` show the missing disk.

Metric: `sermon_raid_degraded`
