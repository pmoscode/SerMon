# HTTP status

The status of the last answer, after redirects.

## Reading it

From the module `http_check`; the label `target` is the configured target (a URL without its query or `host:port`). Targets only within the host's local veto (`module_targets`, `module_hosts`, `module_ports`).

Metric: `sermon_http_check_status_code`
