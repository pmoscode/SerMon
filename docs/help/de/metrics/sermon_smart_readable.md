# SMART lesbar

1, wenn die SMART-Daten der Disk gelesen wurden; 0 im Standby oder wenn sie nicht lesbar sind.

## Einordnung

Gelesen vom Modul `smart` über den Root-Helper (`smartctl --json`, nur lesend); Label `device` ist die Disk.

Metrik: `sermon_smart_readable`
