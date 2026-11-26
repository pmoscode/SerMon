# Apache up

1 if the status page was read, 0 if Apache doesn't answer (stopped, crashed, or the URL points nowhere).

## Reading it

From the module `apache` (`server-status?auto`). Rates from the second run on; after a restart of Apache they skip a run.

Metric: `sermon_apache_up`
