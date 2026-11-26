# Rate

Counters of foreign metrics (counters, `_count` and `_sum` of histograms and summaries) per second, from the second run on. Labels as for `sermon_prometheus_gauge`.

## Reading it

From the module `prometheus`; the label `target` is the endpoint (URL without its query). Endpoints only within the host's `module_targets`, `module_hosts` and `module_ports`.

Metric: `sermon_prometheus_rate`
