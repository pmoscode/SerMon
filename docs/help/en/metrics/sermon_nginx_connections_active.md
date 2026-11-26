# Open connections

Open client connections (`Active connections`), idle ones included. Close to `worker_connections` × workers, nginx soon accepts no more.

## Reading it

From the module `nginx` (stub_status). Rates from the second run on; after a restart of nginx they skip a run.

Metric: `sermon_nginx_connections_active`
