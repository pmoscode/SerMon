# Neustarts des Containers

Neustarts durch seine Restart-Policy (`RestartCount`). Steigt der Wert, stürzt der Container immer wieder ab.

## Einordnung

Vom Modul `docker_socket` (Docker-API, nur lesend). Labels `container` (kurze ID wie bei `docker ps`) und `name` bei den Werten je Container.

Metrik: `sermon_docker_socket_restarts`
