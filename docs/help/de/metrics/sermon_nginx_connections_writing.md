# Verbindungen beim Schreiben

Verbindungen, an die nginx gerade antwortet – auch solche, die auf das Backend warten.

## Einordnung

Vom Modul `nginx` (stub_status). Raten ab dem zweiten Lauf; nach einem Neustart von nginx setzen sie einen Lauf aus.

Metrik: `sermon_nginx_connections_writing`
