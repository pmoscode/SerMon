# Containers by state

All containers by state (`running`, `exited`, `restarting` …), also those not reported one by one.

## Reading it

From the module `docker_socket` (Docker API, read only). Labels `container` (the short ID of `docker ps`) and `name` on the values per container.

Metric: `sermon_docker_socket_containers`
