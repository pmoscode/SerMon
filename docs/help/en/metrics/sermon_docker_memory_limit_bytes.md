# Container memory limit

The container's memory limit (`--memory`); missing without one.

## Reading it

Read by the module `docker` from the cgroups (v2) without the runtime's socket; labels: `container` (short ID as in `docker ps`), `name` (name of the first process), `runtime` (`docker` or `podman`).

Metric: `sermon_docker_memory_limit_bytes`
