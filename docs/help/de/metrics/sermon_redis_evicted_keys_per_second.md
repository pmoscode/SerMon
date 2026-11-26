# Verdrängte Schlüssel

Schlüssel, die Redis je Sekunde wegen `maxmemory` entfernt. Bei einem Cache normal, bei einem Datenspeicher ein Datenverlust.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_evicted_keys_per_second`
