# OOM-Kills im Container

Prozesse, die der OOM-Killer im Container beendet hat, seit er läuft. Steigt der Wert, ist die Speichergrenze zu knapp.

## Einordnung

Gelesen vom Modul `docker` aus den cgroups (v2) ohne den Socket der Runtime; Labels: `container` (kurze ID wie bei `docker ps`), `name` (Name des ersten Prozesses), `runtime` (`docker` oder `podman`).

Metrik: `sermon_docker_oom_kills`
