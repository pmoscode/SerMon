# Standby lag

On a standby: time since the last replayed transaction. Also grows while nothing happens on the primary.

## Reading it

Read by the module `postgres` with a user of the role `pg_monitor`.

Metric: `sermon_postgres_replay_lag_seconds`
