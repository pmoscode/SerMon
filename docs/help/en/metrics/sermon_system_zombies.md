# Zombie processes

Ended processes whose parent hasn't collected the exit status yet.

## Reading it

A few are harmless; if they keep growing, a parent process has a bug.

Metric: `sermon_system_zombies`
