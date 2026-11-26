# Link to the primary

Replicas only: 1 if the link to the primary is up (`master_link_status:up`).

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_master_link_up`
