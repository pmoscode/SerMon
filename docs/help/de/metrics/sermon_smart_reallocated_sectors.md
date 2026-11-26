# Ersetzte Sektoren

ATA-Attribut 5 (SCSI: grown defects): Sektoren, die die Disk durch Reserve ersetzt hat. Steigt der Wert, verschleißt die Disk.

## Einordnung

Gelesen vom Modul `smart` über den Root-Helper (`smartctl --json`, nur lesend); Label `device` ist die Disk.

Metrik: `sermon_smart_reallocated_sectors`
