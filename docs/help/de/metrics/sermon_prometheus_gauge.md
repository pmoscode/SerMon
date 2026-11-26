# Wert

Werte fremder Metriken (Gauges, Quantile, Info, ohne Typ). Label `name` ist der Name beim Endpunkt, `series` seine Labels (`code="200",method="GET"`). Die Einheit kennt nur der Endpunkt.

## Einordnung

Vom Modul `prometheus`; Label `target` ist der Endpunkt (URL ohne Query). Endpunkte nur unter `module_targets`, `module_hosts` und `module_ports` des Hosts.

Metrik: `sermon_prometheus_gauge`
