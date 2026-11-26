# Letzter Snapshot

1, wenn der letzte Snapshot (`BGSAVE`) gelang; 0 meist wegen voller Disk oder fehlender Rechte im Datenverzeichnis.

## Einordnung

Vom Modul `redis` (`INFO`, auch Valkey). Raten ab dem zweiten Lauf; nach einem Neustart von Redis setzen sie einen Lauf aus.

Metrik: `sermon_redis_rdb_last_save_ok`
