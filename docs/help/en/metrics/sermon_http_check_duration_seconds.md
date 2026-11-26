# Response time

Time until the headers of the last answer, redirects included.

## Reading it

From the module `http_check`; the label `target` is the configured target (a URL without its query or `host:port`). Targets only within the host's local veto (`module_targets`, `module_hosts`, `module_ports`).

Metric: `sermon_http_check_duration_seconds`
