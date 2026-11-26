# Container gesund

Nur mit Health-Check: 1 gesund, 0 ungesund; während des Starts kein Wert.

## Einordnung

Vom Modul `docker_socket` (Docker-API, nur lesend). Labels `container` (kurze ID wie bei `docker ps`) und `name` bei den Werten je Container.

Metrik: `sermon_docker_socket_healthy`
