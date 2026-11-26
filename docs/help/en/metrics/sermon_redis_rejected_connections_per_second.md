# Rejected connections

Connections per second Redis refuses because of `maxclients`. Anything above 0 means clients fail.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_rejected_connections_per_second`
