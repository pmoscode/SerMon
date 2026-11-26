# Value

Values of foreign metrics (gauges, quantiles, info, untyped). The label `name` is the endpoint's name, `series` its labels (`code="200",method="GET"`). Only the endpoint knows the unit.

## Reading it

From the module `prometheus`; the label `target` is the endpoint (URL without its query). Endpoints only within the host's `module_targets`, `module_hosts` and `module_ports`.

Metric: `sermon_prometheus_gauge`
