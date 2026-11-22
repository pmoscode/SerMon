# RAID degradiert

1, wenn einem Software-RAID (md) mindestens ein Datenträger fehlt.

## Einordnung

Normal ist 0. Bei 1 läuft das RAID ohne volle Redundanz; ein weiterer Ausfall kann Daten kosten. Den fehlenden Datenträger zeigen `cat /proc/mdstat` und `mdadm --detail`.

Metrik: `sermon_raid_degraded`
