# Worker slots

Slots of the scoreboard (`ServerLimit` × threads per process): as many requests as Apache can serve at once. When the busy workers reach it, requests queue.

## Reading it

From the module `apache` (`server-status?auto`). Rates from the second run on; after a restart of Apache they skip a run.

Metric: `sermon_apache_workers_max`
