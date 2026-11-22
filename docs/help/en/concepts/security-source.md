# Source of security events

The agent reads SSH logins, sudo, account changes, OOM kills and reboots from the logs of the host. Without a source it reports none of them – the security alerts stay quiet although something happens.

## Which source

- **journald** (most distributions): every line carries its sender; the events are verified.
- **SerMon template for rsyslog** (`/var/log/sermon/security.log`): the lines carry the user ID of the sender; events from root are verified.
- **Classic log file** (`/var/log/auth.log`, `/var/log/secure`, `/var/log/messages`): the events count but are [not verified](help:concepts/unverified-source).
- **None:** no journald and no file the agent may read.

Without journald the agent recognizes reboots by the kernel's boot ID, so they arrive even without a readable log file. The node page shows a new source once the agent restarted after the setup.

## What to do

- **Alpine with busybox syslogd:** `/var/log/messages` belongs to the group `wheel`. Install the agent with `install.sh --grant-log-access` (or run the script again with it): syslogd then writes with the group `sermon`.
- **rsyslog:** set up the SerMon template (see [unverified source](help:concepts/unverified-source)); then the events are verified too.
- Then restart the agent (`rc-service sermon-agent restart` or `systemctl restart sermon-agent`).
