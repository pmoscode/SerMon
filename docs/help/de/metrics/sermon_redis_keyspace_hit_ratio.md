# Trefferquote

Anteil der Treffer an allen Abfragen von Schlüsseln seit dem letzten Lauf. Fällt sie bei einem Cache, fehlen Daten im Cache (zu klein, zu kurze TTL).

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_keyspace_hit_ratio`
