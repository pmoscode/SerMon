# Connection limit

`max_connections`. When the connections near it, new ones fail.

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_connections_limit`
