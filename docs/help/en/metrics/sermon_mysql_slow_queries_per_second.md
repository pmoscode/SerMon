# Slow statements

Statements per second that ran longer than `long_query_time` (10 s by default).

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_slow_queries_per_second`
