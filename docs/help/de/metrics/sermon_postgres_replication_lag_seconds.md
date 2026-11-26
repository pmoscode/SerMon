# Replikations-Lag

Wie weit jeder Standby beim Einspielen zurückliegt (`replay_lag` auf dem Primary), höchstens 8 Standbys; Label `replica` ist ihr `application_name` oder ihre Adresse.

## Einordnung

Gelesen vom Modul `postgres` mit einem User der Rolle `pg_monitor`.

Metrik: `sermon_postgres_replication_lag_seconds`
