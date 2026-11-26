# Replication running

1: both replication threads (IO and SQL) run; 0: the replication stopped.

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_replication_running`
