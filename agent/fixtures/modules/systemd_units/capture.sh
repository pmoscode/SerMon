#!/bin/sh
# Captures what the module systemd_units reads (M5-10): `systemctl show` with the module's fixed
# properties for a running service, a service that keeps restarting, a failed one, an instance of
# a template, a timer and a unit that doesn't exist. The hosts are the E2E images with systemd as
# PID 1 (deploy/e2e/agent.Dockerfile).
# Usage: agent/fixtures/modules/systemd_units/capture.sh [distro …]  (Docker, --privileged)
set -eu
cd "$(dirname "$0")"
distros=${*:-debian-13 ubuntu-24.04 rockylinux-9 almalinux-10 fedora-44}
properties=Id,LoadState,ActiveState,SubState,NRestarts

for name in $distros; do
	container="capture-units-$name"
	docker rm -f "$container" >/dev/null 2>&1 || true
	docker run -d --name "$container" --privileged --cgroupns=host \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
		"sermon-e2e-agent-$(printf '%s' "$name" | tr '.' '-')" >/dev/null
	sleep 5
	docker exec "$container" sh -c '
		systemd-run --unit=crashy -p Restart=always -p RestartSec=1 -p StartLimitBurst=100 /bin/sh -c "exit 3" >/dev/null 2>&1
		systemd-run --unit=broken /bin/false >/dev/null 2>&1
		systemd-run --unit=worker@a1 /bin/sleep 600 >/dev/null 2>&1
		sleep 6'
	mkdir -p "$name"
	docker exec "$container" systemctl show --no-pager --property="$properties" -- \
		dbus.service crashy.service broken.service worker@a1.service systemd-tmpfiles-clean.timer \
		missing.service >"$name/show.txt"
	docker rm -f "$container" >/dev/null
	echo "✓ $name → agent/fixtures/modules/systemd_units/$name/"
done
