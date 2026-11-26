# Abgelaufene Schlüssel

Schlüssel, deren TTL ablief, je Sekunde.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_expired_keys_per_second`
