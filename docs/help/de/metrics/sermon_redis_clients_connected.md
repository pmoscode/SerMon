# Verbundene Clients

Verbundene Clients (`connected_clients`); nahe `maxclients` werden neue abgewiesen.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_clients_connected`
