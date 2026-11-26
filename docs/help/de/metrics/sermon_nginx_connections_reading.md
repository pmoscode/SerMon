# Verbindungen beim Lesen

Verbindungen, deren Anfrage nginx gerade liest. Viele davon über längere Zeit deuten auf langsame Clients (oder Slowloris).

## Einordnung

Vom Modul `nginx` (stub_status). Raten ab dem zweiten Lauf; nach einem Neustart von nginx setzen sie einen Lauf aus.

Metrik: `sermon_nginx_connections_reading`
