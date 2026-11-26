# Container bytes received

Bytes received per second in its own network namespace; missing for containers with the host's network (`--network host`).

## Reading it

Read by the module `docker` from the cgroups (v2) without the runtime's socket; labels: `container` (short ID as in `docker ps`), `name` (name of the first process), `runtime` (`docker` or `podman`).

Metric: `sermon_docker_receive_bytes_per_second`
