# Cache hits

Share of blocks served from the buffer cache instead of the disk; only in intervals with reads. Constantly below about 99 % suggests too little `shared_buffers` or memory.

## Reading it

Read by the module `postgres` with a user of the role `pg_monitor`.

Metric: `sermon_postgres_cache_hit_ratio`
