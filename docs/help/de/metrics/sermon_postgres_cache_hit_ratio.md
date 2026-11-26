# Cache-Treffer

Anteil der Blöcke, die aus dem Puffer-Cache kamen statt von der Disk; nur in Intervallen mit Lesezugriffen. Dauerhaft unter etwa 99 % spricht für zu wenig `shared_buffers` oder Speicher.

## Einordnung

Gelesen vom Modul `postgres` mit einem User der Rolle `pg_monitor`.

Metrik: `sermon_postgres_cache_hit_ratio`
