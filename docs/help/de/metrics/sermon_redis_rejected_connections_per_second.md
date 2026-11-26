# Abgewiesene Verbindungen

Verbindungen je Sekunde, die Redis wegen `maxclients` abweist. Jeder Wert über 0 heißt, dass Clients scheitern.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_rejected_connections_per_second`
