# Load per CPU

The average number of processes that wanted to run or waited for disks in the last minute – divided by the number of CPU cores.

## Reading it

- Around 1.0 every core is busy; below that there is headroom.
- Well above 1.0 processes are waiting: the server responds more slowly.
- High load with low CPU busy usually points to slow disks or network storage (processes in state “D”).

## Next steps

1. Compare the load over 1, 5 and 15 minutes in the node detail: is it rising or calming down?
2. Compare CPU busy and iowait.
