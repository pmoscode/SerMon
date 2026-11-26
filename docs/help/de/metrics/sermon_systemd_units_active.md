# Unit aktiv

1, wenn die Unit aktiv ist (`ActiveState=active`), sonst 0. Ein Timer ist aktiv, solange er wartet; ein Dienst in einer Neustart-Schleife ist meist nicht aktiv.

## Einordnung

Vom Modul `systemd_units`; Label `unit` ist die konfigurierte Unit.

Metrik: `sermon_systemd_units_active`
