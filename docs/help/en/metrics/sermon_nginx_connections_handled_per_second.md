# Handled connections

Handled connections per second; equal to the accepted ones while nginx has connections to spare.

## Reading it

From the module `nginx` (stub_status). Rates from the second run on; after a restart of nginx they skip a run.

Metric: `sermon_nginx_connections_handled_per_second`
