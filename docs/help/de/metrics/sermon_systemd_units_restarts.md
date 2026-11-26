# Automatische Neustarts

Nur Dienste: wie oft systemd den Dienst seit dem letzten Start von Hand neu gestartet hat (`NRestarts`, `Restart=`). Steigt der Wert stetig, stürzt der Dienst immer wieder ab.

## Einordnung

Vom Modul `systemd_units`; Label `unit` ist die konfigurierte Unit.

Metrik: `sermon_systemd_units_restarts`
