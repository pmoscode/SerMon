# Container healthy

With a health check only: 1 healthy, 0 unhealthy; no value while starting.

## Reading it

From the module `docker_socket` (Docker API, read only). Labels `container` (the short ID of `docker ps`) and `name` on the values per container.

Metric: `sermon_docker_socket_healthy`
