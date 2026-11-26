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

- **Unix-Socket** (`/run/mysqld/mysqld.sock`; bei RHEL liegt er unter `/var/lib/mysql`, dann `module_sockets` in
  `agent.toml` erweitern) ist der beste Weg. Sonst **Adresse** `host:port` (ab Werk `127.0.0.1:3306`).
- **TLS:** `off`, `verify` (Zertifikat gegen die CAs des Systems oder die **CA-Datei** unter `module_file_prefixes`)
  oder `no_verify` (verschlüsselt, aber an jeden, der antwortet). Leer heißt: `off` auf Loopback und am Socket,
  `verify` für jede andere Adresse. Die selbst erzeugten Zertifikate von MySQL nennen weder Host noch IP und bestehen
  `verify` nicht; dafür ein Zertifikat mit dem Namen oder der IP des Servers einrichten.
- **MySQL 8 und 9** melden mit `caching_sha2_password` an: Die erste Anmeldung nach einem Neustart des Servers
  schickt das Passwort selbst und geht deshalb nur über den Socket oder mit `tls: verify` – nicht mit `no_verify`, denn
  dann bekäme es jeder, der antwortet. Danach reicht der schnelle Weg ohne TLS. Ein falsches Passwort sieht ohne TLS
  genauso aus wie diese erste Anmeldung – die Meldung nennt beides.
- **MariaDB** nutzt `mysql_native_password` und geht auch ohne TLS.
- **Fremde am Port:** Ist der Server gestoppt, kann ein anderer lokaler Benutzer den Port `3306` belegen und sich als
  Datenbank ausgeben. Mit `mysql_native_password` bekommt er kein Passwort, aber eine Antwort, aus der sich ein
  schwaches Passwort durch Durchprobieren erraten lässt. Deshalb: Socket bevorzugen, ein langes zufälliges Passwort und
  einen User nur mit den Rechten oben.

## Metriken

- `sermon_mysql_connections`, `sermon_mysql_connections_running`, `sermon_mysql_connections_limit`,
  `sermon_mysql_aborted_connects_per_second`.
- `sermon_mysql_queries_per_second`, `sermon_mysql_slow_queries_per_second` (über `long_query_time`),
  `sermon_mysql_long_queries` (laufen gerade länger als 60 s).
- `sermon_mysql_received_bytes_per_second`, `sermon_mysql_sent_bytes_per_second`.
- InnoDB: `sermon_mysql_innodb_buffer_pool_bytes`, `…_utilization_ratio`, `…_hit_ratio`.
- Replikation: `sermon_mysql_replica`, `sermon_mysql_replication_running`, `sermon_mysql_replication_lag_seconds`.
