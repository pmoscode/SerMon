# Docker über den Socket

Zustand, Health-Checks und Neustarts der Container aus der Docker-API (auch der kompatiblen API von Podman). Das Modul
ist ab Werk aus und schlägt sich nicht selbst vor.

> **Warnung:** Wer den Docker-Socket benutzen darf, ist root auf dem Host. Das Modul liest nur (zwei Arten von
> `GET`-Anfragen: die Liste der Container und die Auskunft über einen Container), aber ein kompromittierter Agent oder
> Server könnte über den Socket alles. Ohne Socket liefert das Modul **docker** CPU, Speicher, IO und Netz der
> Container aus den cgroups.

## Voraussetzungen

- Auf dem Host in `[local]` von `/etc/sermon/agent.toml`: `allow_docker_socket = true` (ab Werk aus; gilt auch für
  `podman.sock` und `containerd.sock`) und der Socket unter `module_sockets` (ab Werk `/run`, `/var/run`).
- Der User `sermon` braucht Zugriff auf den Socket, etwa über die Gruppe `docker` – auch das entspricht root.

## Einstellungen

- **Socket:** ab Werk `/var/run/docker.sock`; Podman (root): `/run/podman/podman.sock`.
- **Höchstens Container:** ab Werk 30, laufende zuerst.
- **Timeout** je Anfrage.

## Metriken

- `sermon_docker_socket_containers` nach Zustand, `sermon_docker_socket_truncated`.
- Je Container: `sermon_docker_socket_running`, `sermon_docker_socket_healthy` (mit Health-Check),
  `sermon_docker_socket_restarts`, `sermon_docker_socket_oom_killed`.

## Wenn es nicht läuft

- Zustand `vetoed` mit „allow_docker_socket“: Der Host erlaubt den Socket nicht.
- `sermon_docker_socket_up` 0: Der Daemon läuft nicht, der Socket fehlt oder `sermon` darf ihn nicht öffnen.
