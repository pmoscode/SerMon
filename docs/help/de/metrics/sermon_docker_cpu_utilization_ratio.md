# CPU des Containers

CPU-Zeit des Containers als Anteil aller CPUs des Hosts (100 % = alle Kerne ausgelastet).

## Einordnung

Gelesen vom Modul `docker` aus den cgroups (v2) ohne den Socket der Runtime; Labels: `container` (kurze ID wie bei `docker ps`), `name` (Name des ersten Prozesses), `runtime` (`docker` oder `podman`).

Metrik: `sermon_docker_cpu_utilization_ratio`
