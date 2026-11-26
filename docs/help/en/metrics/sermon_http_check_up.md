# Endpoint up

1 if the endpoint answered with an expected status, else 0.

## Reading it

From the module `http_check`; the label `target` is the configured target (a URL without its query or `host:port`). Targets only within the host's local veto (`module_targets`).

Metric: `sermon_http_check_up`
