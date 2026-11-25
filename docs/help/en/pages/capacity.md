# Capacity

Where space runs short and which nodes need more CPU and memory than a month ago.

## File systems by forecast

Every file system of the fleet, those running full first: SerMon fits a line through the free space of the last 24
hours and says when it reaches zero ([forecast](help:metrics/sermon_filesystem_avail_bytes)). Without a falling trend
there is no forecast; these file systems follow by usage. Read-only ones get none. At most 500 are shown.

## CPU and memory over 30 days

Per node the mean of the last 7 days and the change against the first 7 of the 30 days, in percentage points
(40 % → 50 % is +10 %). The course shows the daily means. **By CPU** and **By memory** put the top consumers
first. A click on the node opens its page with the charts.

## Typical next steps

1. Clean up or grow a file system with a near forecast; the default rule warns as well.
2. Check a node whose load rose a lot: new services, a leak, more users?
