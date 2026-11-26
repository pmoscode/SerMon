# Container nach Zustand

Alle Container nach Zustand (`running`, `exited`, `restarting` …), auch die nicht einzeln gemeldeten.

## Einordnung

Vom Modul `docker_socket` (Docker-API, nur lesend). Labels `container` (kurze ID wie bei `docker ps`) und `name` bei den Werten je Container.

Metrik: `sermon_docker_socket_containers`
