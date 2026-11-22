# Time offset

The last clock offset the NTP service passed to the kernel.

## Reading it

Synchronised hosts are usually below one millisecond, over the internet at a few milliseconds. More than 100 ms for long points to a poor time source or a congested network.

Metric: `sermon_time_offset_seconds`
