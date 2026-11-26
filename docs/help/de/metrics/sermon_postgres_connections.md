# Verbindungen zu PostgreSQL

Client-Verbindungen nach Zustand: `active` (fragt gerade), `idle`, `idle_in_transaction` (offene Transaktion ohne Abfrage – hält Sperren und behindert VACUUM), `other`.

## Einordnung

Gelesen vom Modul `postgres` mit einem User der Rolle `pg_monitor`.

Metrik: `sermon_postgres_connections`
