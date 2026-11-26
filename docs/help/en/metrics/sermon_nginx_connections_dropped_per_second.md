# Dropped connections

Accepted but not handled, per second: `worker_connections` is exhausted. Anything above 0 means clients are turned away.

## Reading it

From the module `nginx` (stub_status). Rates from the second run on; after a restart of nginx they skip a run.

Metric: `sermon_nginx_connections_dropped_per_second`
