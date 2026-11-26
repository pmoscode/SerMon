# Speicher des Prozesses

Speicher, den das Betriebssystem Redis zuordnet (`used_memory_rss`); viel mehr als der belegte Speicher heißt Fragmentierung.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_memory_rss_bytes`
