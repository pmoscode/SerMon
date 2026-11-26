# Vom OOM-Killer beendet

1, wenn der Kernel den Container zuletzt wegen Speichermangels beendete (Limit zu klein oder ein Leck).

## Einordnung

Vom Modul `docker_socket` (Docker-API, nur lesend). Labels `container` (kurze ID wie bei `docker ps`) und `name` bei den Werten je Container.

Metrik: `sermon_docker_socket_oom_killed`
