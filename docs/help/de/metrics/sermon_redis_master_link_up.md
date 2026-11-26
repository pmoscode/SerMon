# Verbindung zum Primary

Nur Replikas: 1, wenn die Verbindung zum Primary steht (`master_link_status:up`).

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_master_link_up`
