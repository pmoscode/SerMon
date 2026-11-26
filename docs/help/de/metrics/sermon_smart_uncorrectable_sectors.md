# Nicht korrigierbare Sektoren

ATA-Attribut 198: Sektoren mit Fehlern, die die Disk nicht korrigieren konnte.

## Einordnung

Gelesen vom Modul `smart` über den Root-Helper (`smartctl --json`, nur lesend); Label `device` ist die Disk.

Metrik: `sermon_smart_uncorrectable_sectors`
