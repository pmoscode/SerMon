# Container ausgelassen

1: Es laufen mehr Container, als das Modul meldet (`max_containers`, höchstens 28); gemeldet werden die größten nach Speicher.

## Einordnung

Gelesen vom Modul `docker` aus den cgroups (v2) ohne den Socket der Runtime.

Metrik: `sermon_docker_truncated`
