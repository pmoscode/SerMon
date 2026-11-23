# Log entries dropped

Log entries the agent did not send since it started, by reason:

- `rate`: the host wrote more than 500 entries or 512 KiB per second (beyond a burst).
- `buffer`: logs already took half of the buffer because the server did not accept them for a while. Metrics and
  security events keep their room.

## Reading it

- 0 is normal.
- `rate` points to a service that logs a lot: exclude it in the log settings of the group or raise the minimum
  severity.
- `buffer` mostly means that the server or the connection was unavailable for a longer time.

Metric: `sermon_agent_logs_dropped_entries`
