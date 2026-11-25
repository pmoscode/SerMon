# Available updates

How many packages of the node can be updated according to its last report, security updates included.

## Context

The agent lists the updates every 6 hours, after each run of the package manager and on request; the server keeps
writing the number in between. Without the root helper or when listing fails, the series is missing. Many updates are
no problem as such; what matters are the [security updates](help:metrics/sermon_updates_security_pending).

Metric: `sermon_updates_pending`
