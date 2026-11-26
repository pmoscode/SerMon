# Connection limit

The server's `max_connections`. When the connections near it, new ones fail.

## Reading it

Read by the module `postgres` with a user of the role `pg_monitor`.

Metric: `sermon_postgres_connections_limit`
