# Log entries redacted

Log entries in which the agent replaced something with `***` before sending, since it started: passwords, tokens, keys
and matches of the organization's redaction rules.

## Reading it

A rising value is no error, it shows that secrets end up in logs. Then it is worth configuring the service so that it
does not write them in the first place.

Metric: `sermon_agent_logs_redacted_entries`
