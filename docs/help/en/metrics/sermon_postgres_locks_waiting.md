# Locks waited for

Locks a session waits for. If it stays high, one transaction blocks others.

## Reading it

Read by the module `postgres` with a user of the role `pg_monitor`.

Metric: `sermon_postgres_locks_waiting`
