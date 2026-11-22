# Missing RAID disks

How many disks the array lacks for full redundancy.

## Reading it

Normal is 0. RAID 1 and 5 survive the loss of one disk, RAID 6 of two; more means data loss.

Metric: `sermon_raid_disks_missing`
