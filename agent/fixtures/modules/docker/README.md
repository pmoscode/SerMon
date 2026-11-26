# Docker-Fixtures (M5-16)

Was das Modul `docker` aus cgroup v2 liest, aufgenommen mit `capture.sh` (Docker, arm64): je Container-cgroup
`cpu.stat`, `memory.{current,max,stat,events}`, `io.stat`, `pids.current`, `cgroup.procs`, dazu vom ersten Prozess
`/proc/<pid>/{comm,net/dev}`, `/proc/net/dev` des Hosts und die CPU-Zeilen von `/proc/stat`; zweimal im Abstand von
5 s (`t0`, `t1`) für die Raten. Die IDs sind durch lesbare ersetzt (`d0c1…`, `90d1…`).

| Verzeichnis | Quelle |
|---|---|
| `docker/` | Docker Desktop (Treiber cgroupfs, `docker/<id>`): ein Webserver (busybox `httpd`), ein Client, der von ihm lädt, und ein Container mit 256 MiB Grenze, der auf die Disk schreibt |
| `podman/` | Podman im privilegierten Container `quay.io/podman/stable` (cgroupfs, `libpod_parent/libpod-<id>`): eine Dauerschleife und ein `sleep`; beide im Netz-Namensraum dieses Containers (`netns=host`), also ohne eigenen Verkehr |

Die Pfade der systemd-Treiber (`system.slice/docker-<id>.scope`, `machine.slice/libpod-<id>.scope` mit dem
Unter-cgroup `container`, rootless unter `user.slice/user-<uid>.slice/user@<uid>.service/user.slice/`) bauen die Tests
aus denselben Dateien (`src/docker/tests.rs`); der Inhalt der Dateien ist bei allen Treibern gleich.
