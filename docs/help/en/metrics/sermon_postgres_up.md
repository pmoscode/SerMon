# PostgreSQL up

1 if the server accepted the connection and answered, 0 if nothing accepts the connection (stopped, crashed, wrong
port or socket).

## Reading it

From the module `postgres`. A refused login or missing rights don't count as 0: the module's health names them with
the server's reason. The default rule "PostgreSQL down" reports 0 after 2 minutes.

Metric: `sermon_postgres_up`
