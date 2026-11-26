# MySQL/MariaDB

Verbindungen, Anweisungen, langsame und lange laufende Anweisungen, Datenverkehr, der InnoDB-Buffer-Pool und die
Replikation von MySQL (8.4, 9) und MariaDB (10.11, 11.4, 11.8). Das Modul ist ab Werk aus, fragt alle 30 Sekunden
in einer kurzen Sitzung und braucht dafür einen eigenen User mit wenigen Rechten.

## Monitoring-User anlegen

MySQL:

```
CREATE USER 'sermon'@'localhost' IDENTIFIED BY '…';
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'sermon'@'localhost';
```

MariaDB (ab 10.5 heißt das Recht für den Status der Replikation `REPLICA MONITOR`):

```
CREATE USER 'sermon'@'localhost' IDENTIFIED BY '…';
GRANT PROCESS, REPLICA MONITOR ON *.* TO 'sermon'@'localhost';
```

`PROCESS` zeigt die laufenden Anweisungen aller User (ohne ihre Daten), `REPLICATION CLIENT` den Stand der
Replikation. Fehlt ein Recht, meldet das Modul den Fehler mit dem nötigen `GRANT`.

## Verbindung

- **Adresse** `host:port` (ab Werk `127.0.0.1:3306`) oder **Unix-Socket** (`/run/mysqld/mysqld.sock`; bei RHEL
  liegt er unter `/var/lib/mysql`, dann `module_sockets` in `agent.toml` erweitern).
- **MySQL 8 und 9** melden mit `caching_sha2_password` an: Die erste Anmeldung nach einem Neustart des Servers
  schickt das Passwort selbst und geht deshalb nur über TLS (`tls: no_verify` für das eigene Zertifikat von MySQL) oder
  den Socket; danach reicht der schnelle Weg ohne TLS. Ein falsches Passwort sieht ohne TLS genauso aus wie diese erste
  Anmeldung – die Meldung nennt beides.
- **MariaDB** nutzt `mysql_native_password` und geht auch ohne TLS.

## Metriken

- `sermon_mysql_connections`, `sermon_mysql_connections_running`, `sermon_mysql_connections_limit`,
  `sermon_mysql_aborted_connects_per_second`.
- `sermon_mysql_queries_per_second`, `sermon_mysql_slow_queries_per_second` (über `long_query_time`),
  `sermon_mysql_long_queries` (laufen gerade länger als 60 s).
- `sermon_mysql_received_bytes_per_second`, `sermon_mysql_sent_bytes_per_second`.
- InnoDB: `sermon_mysql_innodb_buffer_pool_bytes`, `…_utilization_ratio`, `…_hit_ratio`.
- Replikation: `sermon_mysql_replica`, `sermon_mysql_replication_running`, `sermon_mysql_replication_lag_seconds`.
