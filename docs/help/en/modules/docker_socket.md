# Docker over the socket

State, health checks and restarts of the containers from the Docker API, on request their output as logs (also Podman's compatible API). The module is
off by default and doesn't suggest itself.

> **Warning:** whoever may use the Docker socket is root on the host. The module only reads (three kinds of `GET`
> request: the list of containers, the inspection of one and – only for the containers under **Container logs** – its
> output), but a compromised agent or server could do anything through the socket. The containers' output leaves the
> host (after redaction): don't name containers that print secrets. Without the socket, the module **docker** reports CPU, memory, IO and network of the containers
> from the cgroups.

## Requirements

- On the host in `[local]` of `/etc/sermon/agent.toml`: `allow_docker_socket = true` (off by default; covers every
  socket of a container runtime: Podman, containerd, CRI-O, BuildKit, libvirt, LXD/Incus) and the socket below `module_sockets` (by default `/run`, `/var/run`).
- The user `sermon` needs access to the socket, e.g. through the group `docker` – that too is root-equivalent.

## Settings

- **Socket:** by default `/var/run/docker.sock`; Podman (root): `/run/podman/podman.sock`.
- **Most containers:** by default 30, running ones first.
- **Timeout** per request.
- **Container logs:** names of the containers whose output (stdout and stderr) goes to the logs, `*` as a wildcard
  (e.g. `web`, `app-*`); at most 16 containers. Empty means no logs. The lines pass the same redaction, selection
  (logs on, excluded units, minimum severity) and limits as the journal and files; in the log explorer under the
  source **Container**, the unit is the container's name. A container starts with the run that first sees it – the
  module fetches no older lines. Limits: the containers together take at most half of the node's log rate (the other
  half stays the host journal's), a run at most 312 lines per container (the rest comes with the next run) and a
  request at most the last 1,000 lines. If lines go missing because of that, `sermon_docker_socket_log_gaps` counts
  it.

## Metrics

- `sermon_docker_socket_containers` by state, `sermon_docker_socket_truncated`.
- Per container: `sermon_docker_socket_running`, `sermon_docker_socket_healthy` (with a health check),
  `sermon_docker_socket_restarts`, `sermon_docker_socket_oom_killed`.
- With container logs: `sermon_docker_socket_log_gaps`.

## When it doesn't work

- State `vetoed` with "allow_docker_socket": the host doesn't allow the socket.
- `sermon_docker_socket_up` 0: the daemon isn't running, the socket is missing or `sermon` may not open it.
