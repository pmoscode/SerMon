# Time synchronised

1 when the kernel considers the clock synchronised (an NTP service steers it).

## Reading it

Normal is 1. At 0 no time synchronisation (chrony, ntpd, systemd-timesyncd) runs or it reaches no server; the clock then drifts.

Metric: `sermon_time_synchronized`
