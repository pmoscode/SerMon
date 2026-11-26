# Redis erreichbar

1, wenn `INFO` gelesen wurde, 0, wenn Redis nicht antwortet.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_up`
