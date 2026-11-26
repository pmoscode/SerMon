# nginx up

1 if the stub_status page was read, 0 if nginx doesn't answer (stopped, crashed, or the URL points nowhere).

## Reading it

From the module `nginx` (stub_status). Rates from the second run on; after a restart of nginx they skip a run.

Metric: `sermon_nginx_up`
