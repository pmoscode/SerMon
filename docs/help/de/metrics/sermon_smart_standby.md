# Disk im Standby

1, wenn die Disk schläft; der Agent weckt sie nicht.

## Einordnung

Gelesen vom Modul `smart` über den Root-Helper (`smartctl --json`, nur lesend); Label `device` ist die Disk.

Metrik: `sermon_smart_standby`
