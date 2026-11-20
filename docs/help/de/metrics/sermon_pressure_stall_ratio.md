# Pressure Stall Information (PSI)

Anteil der Zeit, in der Aufgaben auf eine Ressource warten mussten (Label `resource`: cpu, memory, io; `kind`: some = einige Aufgaben, full = alle).

## Einordnung

PSI zeigt Engpässe direkter als Auslastungswerte: schon wenige Prozent `full` bei memory oder io sind spürbar.

Metrik: `sermon_pressure_stall_ratio`
