# Last snapshot

1 if the last snapshot (`BGSAVE`) succeeded; 0 mostly for a full disk or missing rights in the data directory.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_rdb_last_save_ok`
