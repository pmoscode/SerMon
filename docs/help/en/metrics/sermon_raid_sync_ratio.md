# RAID sync

Progress of a running sync: resync, recovery, reshape or check.

## Reading it

The metric exists only while a sync runs. After replacing a disk it shows when the array is redundant again. A regular `check` (e.g. monthly by cron) is normal.

Metric: `sermon_raid_sync_ratio`
