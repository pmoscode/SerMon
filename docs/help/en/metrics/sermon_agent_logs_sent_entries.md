# Log entries sent

Log entries the agent has written to its buffer for sending since it started: from the journal or the log file, after
the log settings of the group and after redaction.

## Reading it

- The value rises with the amount of logs on the host.
- If it stays at 0, logs are off for the group, the agent forbids them locally (`allow_logs = false` in
  `/etc/sermon/agent.toml`), or it has no configuration from the server yet.

Metric: `sermon_agent_logs_sent_entries`
