# Verbundene Replikas

Replikas eines Primary (`connected_slaves`). Sinkt die Zahl, ist eine Replika weg.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_connected_replicas`
