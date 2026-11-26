# Ended by the OOM killer

1 if the kernel ended the container last for lack of memory (limit too small or a leak).

## Reading it

From the module `docker_socket` (Docker API, read only). Labels `container` (the short ID of `docker ps`) and `name` on the values per container.

Metric: `sermon_docker_socket_oom_killed`
