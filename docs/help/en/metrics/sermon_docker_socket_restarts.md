# Container restarts

Restarts by its restart policy (`RestartCount`). If it rises, the container keeps crashing.

## Reading it

From the module `docker_socket` (Docker API, read only). Labels `container` (the short ID of `docker ps`) and `name` on the values per container.

Metric: `sermon_docker_socket_restarts`
