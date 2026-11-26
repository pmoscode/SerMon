# Unit fehlgeschlagen

1, wenn die Unit fehlgeschlagen ist (`ActiveState=failed`), sonst 0. Ursache im Journal der Unit (`journalctl -u <unit>`).

## Einordnung

Vom Modul `systemd_units`; Label `unit` ist die konfigurierte Unit.

Metrik: `sermon_systemd_units_failed`
