# Docker

CPU, Speicher, Disk-IO, Netz und Prozesse je Container von Docker und Podman, gelesen aus den cgroups (v2) – ohne
den Socket der Runtime und ohne Zusatzrechte. Das Modul ist ab Werk aus.

## Was es sieht

- Container von Docker und Podman mit den Treibern `systemd` und `cgroupfs`, auch rootless.
- Als Namen den Namen des ersten Prozesses im Container (etwa `nginx` oder `postgres`); die Namen aus `docker ps`
  kennt nur der Socket. Das Label `container` ist die kurze ID wie bei `docker ps`.
- Höchstens 28 Container (**Höchstens Container**, ab Werk 20), die größten nach Speicher zuerst; sind es mehr, ist
  `sermon_docker_truncated` 1.
- Raten (CPU, IO, Netz) ab dem zweiten Lauf eines Containers.
- Container mit dem Netz des Hosts (`--network host`) melden keinen eigenen Verkehr – er wäre der des Hosts.

## Voraussetzungen

cgroup v2 (alle unterstützten Distributionen). Unter cgroup v1 meldet das Modul einen Fehler.

## Metriken

- `sermon_docker_cpu_utilization_ratio`: Anteil an allen CPUs des Hosts.
- `sermon_docker_memory_used_bytes`, `sermon_docker_memory_limit_bytes`: Speicher wie bei `docker stats` und die
  Grenze (nur mit einer).
- `sermon_docker_oom_kills`: Prozesse, die der OOM-Killer im Container beendet hat.
- `sermon_docker_read_bytes_per_second`, `sermon_docker_write_bytes_per_second`: Disk-IO.
- `sermon_docker_receive_bytes_per_second`, `sermon_docker_transmit_bytes_per_second`: Netz.
- `sermon_docker_processes`, `sermon_docker_containers`.
