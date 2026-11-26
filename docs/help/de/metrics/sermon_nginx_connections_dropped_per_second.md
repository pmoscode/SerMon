# Abgewiesene Verbindungen

Angenommen, aber nicht bearbeitet, je Sekunde: `worker_connections` ist ausgeschöpft. Jeder Wert über 0 heißt, dass Clients abgewiesen werden.

## Einordnung

Vom Modul `nginx` (stub_status). Raten ab dem zweiten Lauf; nach einem Neustart von nginx setzen sie einen Lauf aus.

Metrik: `sermon_nginx_connections_dropped_per_second`
