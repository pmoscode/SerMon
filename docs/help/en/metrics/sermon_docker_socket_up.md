# Docker API up

1 if the Docker API answered, 0 if the daemon isn't running or the socket is missing.

## Reading it

From the module `docker_socket` (Docker API, read only). Labels `container` (the short ID of `docker ps`) and `name` on the values per container.

Metric: `sermon_docker_socket_up`
