# Empfangene Bytes des Containers

Empfangene Bytes je Sekunde in seinem eigenen Netz-Namensraum; fehlt bei Containern mit dem Netz des Hosts (`--network host`).

## Einordnung

Gelesen vom Modul `docker` aus den cgroups (v2) ohne den Socket der Runtime; Labels: `container` (kurze ID wie bei `docker ps`), `name` (Name des ersten Prozesses), `runtime` (`docker` oder `podman`).

Metrik: `sermon_docker_receive_bytes_per_second`
