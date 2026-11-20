#!/bin/sh
# Captures real security log lines per distribution for the parser fixtures (M2-06): a systemd
# container with sshd and sudo runs capture.py and the journal output lands in ../<distro>/.
# Usage: agent/fixtures/logs/capture/capture.sh [image…]  (Docker, containers run --privileged)
set -eu
cd "$(dirname "$0")"
images=${*:-debian:13 ubuntu:24.04 rockylinux:9 rockylinux:8}
for base in $images; do
	name=$(printf '%s' "$base" | tr ':/' '--')
	docker build -q --build-arg BASE="$base" -t "sermon-capture-$name" . >/dev/null
	docker rm -f "capture-$name" >/dev/null 2>&1 || true
	docker run -d --name "capture-$name" --privileged --cgroupns=host \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /tmp "sermon-capture-$name" >/dev/null
	sleep 5
	docker cp capture.py "capture-$name:/root/capture.py"
	docker exec "capture-$name" python3 /root/capture.py
	docker cp "capture-$name:/root/fixtures/." "../$name/"
	docker rm -f "capture-$name" >/dev/null
	echo "✓ $base → agent/fixtures/logs/$name/"
done
