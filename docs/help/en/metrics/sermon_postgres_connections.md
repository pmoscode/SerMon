# PostgreSQL connections

Client connections by state: `active` (running a query), `idle`, `idle_in_transaction` (an open transaction without a query – holds locks and hinders VACUUM), `other`.

## Reading it

Read by the module `postgres` with a user of the role `pg_monitor`.

Metric: `sermon_postgres_connections`
