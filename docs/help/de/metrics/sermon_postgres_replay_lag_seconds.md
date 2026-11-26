# Lag des Standbys

Auf einem Standby: Zeit seit der zuletzt eingespielten Transaktion. Steigt auch, wenn auf dem Primary nichts geschieht.

## Einordnung

Gelesen vom Modul `postgres` mit einem User der Rolle `pg_monitor`.

Metrik: `sermon_postgres_replay_lag_seconds`
