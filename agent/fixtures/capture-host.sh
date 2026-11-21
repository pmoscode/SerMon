#!/bin/sh
# Captures the files the collectors read (/proc, /sys, /etc) from a container per distribution
# for the agent fixtures (M2-19). /proc and /sys show the kernel of the Docker host; what differs
# per distribution is /etc (os-release) and the layout. Hostname and machine-id are replaced by
# fixture values.
# Usage: agent/fixtures/capture-host.sh image name [image name …]
#   e.g. agent/fixtures/capture-host.sh rockylinux:9 rockylinux-9 almalinux:10 almalinux-10
set -eu
cd "$(dirname "$0")"

files='etc/os-release
proc/diskstats
proc/loadavg
proc/meminfo
proc/net/dev
proc/net/netstat
proc/net/snmp
proc/net/sockstat
proc/pressure/cpu
proc/pressure/io
proc/pressure/memory
proc/self/mountinfo
proc/stat
proc/sys/fs/file-nr
proc/sys/kernel/osrelease
proc/sys/kernel/random/entropy_avail
proc/sys/net/netfilter/nf_conntrack_count
proc/sys/net/netfilter/nf_conntrack_max
proc/uptime
proc/vmstat'

if [ $# -lt 2 ] || [ $(($# % 2)) -ne 0 ]; then
	echo "usage: $0 image name [image name …]" >&2
	exit 2
fi
while [ $# -ge 2 ]; do
	image=$1 name=$2
	shift 2
	container="capture-host-$name"
	docker rm -f "$container" >/dev/null 2>&1 || true
	docker run -d --name "$container" "$image" sleep 600 >/dev/null
	rm -rf "$name"
	echo "$files" | while read -r f; do
		mkdir -p "$name/$(dirname "$f")"
		# os-release may be a link to /usr/lib/os-release; cat follows it.
		docker exec "$container" cat "/$f" >"$name/$f" 2>/dev/null || rm -f "$name/$f"
	done
	for dev in $(docker exec "$container" ls /sys/class/net); do
		mkdir -p "$name/sys/class/net/$dev"
		for attr in operstate speed; do
			docker exec "$container" cat "/sys/class/net/$dev/$attr" >"$name/sys/class/net/$dev/$attr" 2>/dev/null || :
		done
	done
	for dev in $(docker exec "$container" ls /sys/block); do
		mkdir -p "$name/sys/block/$dev" && : >"$name/sys/block/$dev/.keep"
	done
	mkdir -p "$name/proc/sys/kernel"
	echo 0123456789abcdef0123456789abcdef >"$name/etc/machine-id"
	echo web-01 >"$name/proc/sys/kernel/hostname"
	docker rm -f "$container" >/dev/null
	echo "✓ $image → agent/fixtures/$name/"
done
