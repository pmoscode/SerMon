# Idle workers

Workers waiting for a request (`IdleWorkers`). With none left, new requests wait.

## Reading it

From the module `apache` (`server-status?auto`). Rates from the second run on; after a restart of Apache they skip a run.

Metric: `sermon_apache_workers_idle`
