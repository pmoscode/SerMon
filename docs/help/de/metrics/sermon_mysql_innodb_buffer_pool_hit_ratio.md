# Treffer im Buffer-Pool

Anteil der Lesezugriffe, die der Buffer-Pool ohne Disk bediente. Dauerhaft unter etwa 99 %: der Pool ist zu klein.

## Einordnung

Gelesen vom Modul `mysql` (MySQL und MariaDB) mit einem User mit `PROCESS` und `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metrik: `sermon_mysql_innodb_buffer_pool_hit_ratio`
