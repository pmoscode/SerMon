# Replication lag

How far the replica lags behind its source (`Seconds_Behind_Source`); missing while it doesn't replicate.

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_replication_lag_seconds`
