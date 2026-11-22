# RAID-Abgleich

Fortschritt eines laufenden Abgleichs: Resync, Wiederherstellung, Umbau oder Prüfung.

## Einordnung

Die Metrik gibt es nur, solange ein Abgleich läuft. Nach dem Ersetzen eines Datenträgers zeigt sie, wann das RAID wieder redundant ist. Ein regelmäßiger `check` (z. B. monatlich per Cron) ist normal.

Metrik: `sermon_raid_sync_ratio`
