# Series taken

Series of the endpoint that were taken (at most the configured number).

## Reading it

From the module `prometheus`; the label `target` is the endpoint (URL without its query). Endpoints only within the host's `module_targets`, `module_hosts` and `module_ports`.

Metric: `sermon_prometheus_series`
