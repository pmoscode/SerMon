# "n/a" in the live view

The agent could not read the value. It runs as the user `sermon` without special rights and sees of other users' processes only what every user may see.

## Why

- Linux protects parts of `/proc/<pid>` of other users' processes; depending on the setting (`hidepid`) also memory, runtime or command line.
- A process that ends while it is read can arrive incomplete.
- SerMon deliberately doesn't read the open files and I/O of other users' processes: the agent would need `CAP_SYS_PTRACE` for that, and it doesn't get it.

## What to do

Usually nothing – the other values are right. If you need more, look on the host itself with `sudo`.
