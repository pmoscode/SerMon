# Container CPU

CPU time of the container as a share of all the host's CPUs (100 % = every core busy).

## Reading it

Read by the module `docker` from the cgroups (v2) without the runtime's socket; labels: `container` (short ID as in `docker ps`), `name` (name of the first process), `runtime` (`docker` or `podman`).

Metric: `sermon_docker_cpu_utilization_ratio`
