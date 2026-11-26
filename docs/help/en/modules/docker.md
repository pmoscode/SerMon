# Docker

CPU, memory, disk IO, network and processes per container of Docker and Podman, read from the cgroups (v2) –
without the runtime's socket and without extra rights. The module is off by default.

## What it sees

- Containers of Docker and Podman with the drivers `systemd` and `cgroupfs`, rootless too.
- As the name the name of the container's first process (e.g. `nginx` or `postgres`); only the socket knows the names
  of `docker ps`. The label `container` is the short ID as in `docker ps`.
- At most 28 containers (**Most containers**, 20 by default), the largest by memory first; with more,
  `sermon_docker_truncated` is 1.
- Rates (CPU, IO, network) from a container's second run on.
- Containers with the host's network (`--network host`) report no traffic of their own – it would be the host's.

## Requirements

cgroup v2 (every supported distribution). Under cgroup v1 the module reports an error.

## Metrics

- `sermon_docker_cpu_utilization_ratio`: share of all the host's CPUs.
- `sermon_docker_memory_used_bytes`, `sermon_docker_memory_limit_bytes`: memory as in `docker stats` and the limit
  (only with one).
- `sermon_docker_oom_kills`: processes the OOM killer ended in the container.
- `sermon_docker_read_bytes_per_second`, `sermon_docker_write_bytes_per_second`: disk IO.
- `sermon_docker_receive_bytes_per_second`, `sermon_docker_transmit_bytes_per_second`: network.
- `sermon_docker_processes`, `sermon_docker_containers`.
