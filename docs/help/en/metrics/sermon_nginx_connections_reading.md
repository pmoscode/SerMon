# Connections reading

Connections whose request nginx is reading. Many of them for long hint at slow clients (or slowloris).

## Reading it

From the module `nginx` (stub_status). Rates from the second run on; after a restart of nginx they skip a run.

Metric: `sermon_nginx_connections_reading`
