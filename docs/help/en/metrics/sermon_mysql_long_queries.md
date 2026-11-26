# Long-running statements

Statements running for more than 60 s right now (all users; needs `PROCESS`).

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_long_queries`
