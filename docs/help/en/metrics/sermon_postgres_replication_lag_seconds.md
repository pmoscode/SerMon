# Replication lag

How far each standby lags behind in replaying (`replay_lag` on the primary), at most 8 standbys; the label `replica` is their `application_name` or address.

## Reading it

Read by the module `postgres` with a user of the role `pg_monitor`.

Metric: `sermon_postgres_replication_lag_seconds`
