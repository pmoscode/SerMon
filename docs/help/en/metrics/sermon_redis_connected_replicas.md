# Connected replicas

Replicas of a primary (`connected_slaves`). If it drops, a replica is gone.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_connected_replicas`
