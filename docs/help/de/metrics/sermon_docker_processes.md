# Prozesse des Containers

Prozesse und Threads im Container (`pids.current`).

## Einordnung

Gelesen vom Modul `docker` aus den cgroups (v2) ohne den Socket der Runtime; Labels: `container` (kurze ID wie bei `docker ps`), `name` (Name des ersten Prozesses), `runtime` (`docker` oder `podman`).

Metrik: `sermon_docker_processes`
