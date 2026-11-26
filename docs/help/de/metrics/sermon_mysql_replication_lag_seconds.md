# Replikations-Lag

Wie weit die Replica hinter der Quelle liegt (`Seconds_Behind_Source`); fehlt, solange sie nicht repliziert.

## Einordnung

Gelesen vom Modul `mysql` (MySQL und MariaDB) mit einem User mit `PROCESS` und `REPLICATION CLIENT` (MariaDB: `REPLICA MONITOR`).

Metrik: `sermon_mysql_replication_lag_seconds`
