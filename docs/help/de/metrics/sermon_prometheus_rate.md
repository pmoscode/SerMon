# Rate

Zähler fremder Metriken (Counter, `_count` und `_sum` von Histogrammen und Summaries) je Sekunde, ab dem zweiten Lauf. Labels wie bei `sermon_prometheus_gauge`.

## Einordnung

Vom Modul `prometheus`; Label `target` ist der Endpunkt (URL ohne Query). Endpunkte nur unter `module_targets`, `module_hosts` und `module_ports` des Hosts.

Metrik: `sermon_prometheus_rate`
