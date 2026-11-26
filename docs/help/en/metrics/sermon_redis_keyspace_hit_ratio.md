# Hit ratio

Share of hits among all lookups of keys since the last run. Falling for a cache, data is missing from it (too small, TTL too short).

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_keyspace_hit_ratio`
