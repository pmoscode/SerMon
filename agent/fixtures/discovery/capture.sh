#!/bin/sh
# Captures what the discovery of the modules reads (M5-04): the listening sockets of
# /proc/net/{tcp,tcp6,unix}, the process names (/proc/<pid>/comm) and the running services
# (systemctl, as the agent asks it) of a host with nginx and sshd. The hosts are the E2E images
# with systemd as PID 1 (deploy/e2e/agent.Dockerfile); Alpine runs nginx without OpenRC.
# Usage: agent/fixtures/discovery/capture.sh [distro …]  (Docker, containers run --privileged)
set -eu
cd "$(dirname "$0")"
distros=${*:-debian-13 ubuntu-24.04 rockylinux-9 almalinux-10 fedora-44 alpine-3.22}

for name in $distros; do
	container="capture-discovery-$name"
	docker rm -f "$container" >/dev/null 2>&1 || true
	case $name in
	alpine-*)
		docker run -d --name "$container" "alpine:${name#alpine-}" sleep 600 >/dev/null
		docker exec "$container" sh -c 'apk add -q nginx && nginx'
		;;
	*)
		image="sermon-e2e-agent-$(printf '%s' "$name" | tr '.' '-')"
		docker run -d --name "$container" --privileged --cgroupns=host \
			-v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
			"$image" >/dev/null
		sleep 5
		docker exec "$container" sh -c '
			if command -v apt-get >/dev/null; then
				apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx openssh-server >/dev/null
			else
				dnf install -y -q nginx openssh-server >/dev/null
			fi
			systemctl start nginx
			systemctl start ssh 2>/dev/null || systemctl start sshd'
		;;
	esac
	sleep 2
	rm -rf "$name"
	mkdir -p "$name/proc/net"
	for file in tcp tcp6 unix; do
		docker exec "$container" cat "/proc/net/$file" >"$name/proc/net/$file"
	done
	# The connections to the package mirrors: their addresses become documentation addresses
	# (192.0.2.0/24, 2001:db8::/32); unspecified and loopback addresses stay.
	python3 - "$name/proc/net/tcp" "$name/proc/net/tcp6" <<'PY'
import re, sys
keep4 = re.compile(r"^(00000000|[0-9A-F]{6}7F)$")
keep6 = re.compile(r"^(0{32}|0{24}01000000|0{16}FFFF0000[0-9A-F]{6}7F)$")
for path, keep, doc in ((sys.argv[1], keep4, "{:02X}0200C0"), (sys.argv[2], keep6, "B80D01200000000000000000{:02X}000000")):
    lines = open(path).read().split("\n")
    out = [lines[0]]
    for line in lines[1:]:
        def sub(m):
            addr = m.group(1)
            return m.group(0) if keep.match(addr) else doc.format(10 + sum(map(ord, addr)) % 200) + ":" + m.group(2)
        out.append(re.sub(r"\b([0-9A-F]{8}|[0-9A-F]{32}):([0-9A-F]{4})\b", sub, line))
    open(path, "w").write("\n".join(out))
PY
	for pid in $(docker exec "$container" sh -c 'ls /proc | grep -E "^[0-9]+$"'); do
		comm=$(docker exec "$container" cat "/proc/$pid/comm" 2>/dev/null) || continue
		mkdir -p "$name/proc/$pid"
		printf '%s\n' "$comm" >"$name/proc/$pid/comm"
	done
	case $name in
	alpine-*) ;;
	*)
		docker exec "$container" systemctl list-units --type=service --state=running \
			--plain --no-legend --no-pager --full >"$name/units.txt"
		;;
	esac
	docker rm -f "$container" >/dev/null
	echo "✓ $name → agent/fixtures/discovery/$name/"
done
