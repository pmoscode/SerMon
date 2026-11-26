# Process memory

Memory the operating system gives Redis (`used_memory_rss`); much more than the used memory means fragmentation.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_memory_rss_bytes`
