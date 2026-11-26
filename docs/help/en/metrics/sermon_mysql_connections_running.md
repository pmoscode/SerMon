# Running connections

Connections running a statement right now (`Threads_running`). Constantly high: the server can't keep up.

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_connections_running`
