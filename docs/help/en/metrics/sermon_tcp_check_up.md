# Port up

1 if a connection was made, else 0.

## Reading it

From the module `tcp_check`; the label `target` is the configured target (a URL without its query or `host:port`). Targets only within the host's local veto (`module_targets`).

Metric: `sermon_tcp_check_up`
