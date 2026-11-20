# CPU-Auslastung nach Modus

Anteil der Rechenzeit aller Kerne je Modus: `user` (Programme), `system` (Kernel), `iowait` (Warten auf Datenträger), `steal` (vom Hypervisor entzogen), `idle` (untätig) und weitere. Zusammen ergeben die Modi 100 %.

## Einordnung

Viel `iowait` deutet auf langsame Datenträger, viel `steal` auf einen überbuchten Virtualisierungs-Host.

Metrik: `sermon_cpu_utilization_ratio`
