# MySQL/MariaDB

Connections, statements, slow and long-running statements, traffic, the InnoDB buffer pool and the replication of
MySQL (8.4, 9) and MariaDB (10.11, 11.4, 11.8). The module is off by default, asks every 30 seconds in a short session
and needs a user of its own with few rights.

## Create a monitoring user

MySQL:

```
CREATE USER 'sermon'@'localhost' IDENTIFIED BY '…';
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'sermon'@'localhost';
```

MariaDB (from 10.5 the right to see the replication is called `REPLICA MONITOR`):

```
CREATE USER 'sermon'@'localhost' IDENTIFIED BY '…';
GRANT PROCESS, REPLICA MONITOR ON *.* TO 'sermon'@'localhost';
```

`PROCESS` shows the running statements of all users (without their data), `REPLICATION CLIENT` the state of the
replication. If a right is missing, the module reports the error with the `GRANT` it needs.

## Connection

- **Address** `host:port` (`127.0.0.1:3306` by default) or **Unix socket** (`/run/mysqld/mysqld.sock`; on RHEL it is
  below `/var/lib/mysql`, then extend `module_sockets` in `agent.toml`).
- **MySQL 8 and 9** log in with `caching_sha2_password`: the first login after a restart of the server sends the
  password itself and therefore works only over TLS (`tls: no_verify` for MySQL's own certificate) or the socket;
  after that the fast way works without TLS. Without TLS a wrong password looks just like that first login – the
  message names both.
- **MariaDB** uses `mysql_native_password` and works without TLS too.

## Metrics

- `sermon_mysql_connections`, `sermon_mysql_connections_running`, `sermon_mysql_connections_limit`,
  `sermon_mysql_aborted_connects_per_second`.
- `sermon_mysql_queries_per_second`, `sermon_mysql_slow_queries_per_second` (over `long_query_time`),
  `sermon_mysql_long_queries` (running longer than 60 s right now).
- `sermon_mysql_received_bytes_per_second`, `sermon_mysql_sent_bytes_per_second`.
- InnoDB: `sermon_mysql_innodb_buffer_pool_bytes`, `…_utilization_ratio`, `…_hit_ratio`.
- Replication: `sermon_mysql_replica`, `sermon_mysql_replication_running`, `sermon_mysql_replication_lag_seconds`.
