# Log files followed

Log files the agent reads right now because a file pattern of the group's log settings names them (e.g. `/var/log/nginx/*.log`).

## Reading it

- At most 64 files per node. Rotated and compressed files (`access.log.1`, `.gz`) don't count: their lines came under the active name already.
- The agent finds new files within 10 seconds; it reads a new file from its start.
- 0 despite patterns means: no matching file exists, sending logs is off, or the files are outside the allowed directories (see [rejected log files](help:metrics/sermon_agent_log_files_rejected)).

Metric: `sermon_agent_log_files_followed`
