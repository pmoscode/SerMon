# Geschriebene Bytes des Containers

Auf Block-Geräte geschriebene Bytes je Sekunde.

## Einordnung

Gelesen vom Modul `docker` aus den cgroups (v2) ohne den Socket der Runtime; Labels: `container` (kurze ID wie bei `docker ps`), `name` (Name des ersten Prozesses), `runtime` (`docker` oder `podman`).

Metrik: `sermon_docker_write_bytes_per_second`
