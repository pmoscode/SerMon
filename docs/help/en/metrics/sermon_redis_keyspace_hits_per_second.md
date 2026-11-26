# Hits

Lookups of existing keys per second.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_keyspace_hits_per_second`
