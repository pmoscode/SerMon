# Belegter Speicher

Speicher, den Redis für Daten belegt (`used_memory`). Erreicht er `maxmemory`, verdrängt Redis Schlüssel oder lehnt Schreibzugriffe ab.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_memory_used_bytes`
