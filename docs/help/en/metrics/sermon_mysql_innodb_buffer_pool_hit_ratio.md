# Buffer pool hits

Share of reads the buffer pool served without the disk. Constantly below about 99 %: the pool is too small.

## Reading it

Read by the module `mysql` (MySQL and MariaDB) with a user that has `PROCESS` and `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metric: `sermon_mysql_innodb_buffer_pool_hit_ratio`
