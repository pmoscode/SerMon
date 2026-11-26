# Requests

Requests per second from `Total Accesses` (needs `ExtendedStatus On`, the default since Apache 2.4).

## Reading it

From the module `apache` (`server-status?auto`). Rates from the second run on; after a restart of Apache they skip a run.

Metric: `sermon_apache_requests_per_second`
