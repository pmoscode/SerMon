# OOM kills in the container

Processes the OOM killer ended in the container since it runs. If it grows, the memory limit is too tight.

## Reading it

Read by the module `docker` from the cgroups (v2) without the runtime's socket; labels: `container` (short ID as in `docker ps`), `name` (name of the first process), `runtime` (`docker` or `podman`).

Metric: `sermon_docker_oom_kills`
