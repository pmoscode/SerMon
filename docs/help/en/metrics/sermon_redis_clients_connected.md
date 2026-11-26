# Connected clients

Connected clients (`connected_clients`); close to `maxclients` new ones are refused.

## Reading it

From the module `redis` (`INFO`, Valkey too). Rates from the second run on; after a restart of Redis they skip a run.

Metric: `sermon_redis_clients_connected`
