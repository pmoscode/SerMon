# Memory used

Memory Redis uses for data (`used_memory`). At `maxmemory` Redis evicts keys or refuses writes.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_memory_used_bytes`
