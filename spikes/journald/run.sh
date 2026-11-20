#!/bin/sh
# Spike M0-10: journalctl as a child process. Builds a systemd container per distribution, runs
# experiments.py inside and collects fixtures. Usage: spikes/journald/run.sh [distro…]
set -eu
cd "$(dirname "$0")"
distros=${*:-debian:13 ubuntu:24.04 rockylinux:9 rockylinux:8}
mkdir -p results
for base in $distros; do
	name=$(printf '%s' "$base" | tr ':/' '--')
	docker build -q --build-arg BASE="$base" -t "sermon-journald-$name" . >/dev/null
	docker rm -f "journald-$name" >/dev/null 2>&1 || true
	docker run -d --name "journald-$name" --privileged --cgroupns=host \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /tmp "sermon-journald-$name" >/dev/null
	sleep 5
	docker cp experiments.py "journald-$name:/root/experiments.py"
	docker exec "journald-$name" python3 /root/experiments.py >"results/$name.txt" 2>&1 || true
	mkdir -p "results/$name"
	docker cp "journald-$name:/root/fixtures/." "results/$name/" 2>/dev/null || true
	docker rm -f "journald-$name" >/dev/null
	echo "✓ $base → results/$name.txt"
done
