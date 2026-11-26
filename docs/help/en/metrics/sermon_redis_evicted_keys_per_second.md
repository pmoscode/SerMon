# Evicted keys

Keys Redis removes per second because of `maxmemory`. Normal for a cache, data loss for a data store.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_evicted_keys_per_second`
