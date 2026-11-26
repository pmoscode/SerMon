# Docker-API erreichbar

1, wenn die Docker-API antwortete, 0, wenn der Daemon nicht läuft oder der Socket fehlt.

## Einordnung

Vom Modul `docker_socket` (Docker-API, nur lesend). Labels `container` (kurze ID wie bei `docker ps`) und `name` bei den Werten je Container.

Metrik: `sermon_docker_socket_up`
