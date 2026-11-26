# Offene Verbindungen

Offene Verbindungen von Clients (`Active connections`), auch wartende. Nähert sich der Wert `worker_connections` × Worker, nimmt nginx bald keine mehr an.

## Einordnung

Vom Modul `nginx` (stub_status). Raten ab dem zweiten Lauf; nach einem Neustart von nginx setzen sie einen Lauf aus.

Metrik: `sermon_nginx_connections_active`
