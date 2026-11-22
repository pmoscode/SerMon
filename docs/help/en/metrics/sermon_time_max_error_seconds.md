# Maximum time error

The upper bound of the clock error as the kernel estimates it.

## Reading it

While an NTP service steers the clock, the value stays small. Without synchronisation it grows by 0.5 ms per second up to 16 s.

Metric: `sermon_time_max_error_seconds`
