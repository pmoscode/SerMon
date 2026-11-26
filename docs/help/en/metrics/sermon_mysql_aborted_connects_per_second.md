# Failed connections

Failed connection attempts per second (wrong password, missing rights, time-outs).

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_aborted_connects_per_second`
