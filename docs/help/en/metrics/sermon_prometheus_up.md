# Endpoint up

1 if the page was read, 0 if the endpoint doesn't answer.

## Reading it

From the module `prometheus`; the label `target` is the endpoint (URL without its query). Endpoints only within the host's `module_targets`.

Metric: `sermon_prometheus_up`
