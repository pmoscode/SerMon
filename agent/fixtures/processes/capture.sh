#!/bin/sh
# Captures /proc/<pid> of a few special processes for the process collector (M3-13, KONZEPT §9):
# a normal process, a process of another user, a zombie, an executable named with ")" and
# spaces; from the Docker host's PID namespace a kernel thread, a kernel worker and a process with
# many threads, stored as PID 9000 + n (their own PIDs would collide with the container's).
# Also the files of the header (stat, meminfo, loadavg, uptime) and /etc/passwd.
# Usage: agent/fixtures/processes/capture.sh [image] (default debian:13)
set -eu
cd "$(dirname "$0")"
image=${1:-debian:13}
out=debian-13
rm -rf "$out"
mkdir -p "$out"

docker run --rm -v "$PWD/$out:/out" "$image" bash -c '
	set -eu
	sleep 300 &
	normal=$!
	setpriv --reuid=65534 --regid=65534 --clear-groups sleep 300 &
	other=$!
	cp /bin/bash "/tmp/evil) name (x"
	"/tmp/evil) name (x" -c "sleep 300; echo '\''a b'\''" &
	odd=$!
	# A zombie: its parent exec()s into sleep and never reaps it.
	bash -c "sleep 0 & exec sleep 300" &
	parent=$!
	sleep 1
	zombie=$(grep -l "^State:.Z" /proc/[0-9]*/status | head -1 | cut -d/ -f3)
	for pid in 1 $normal $other $odd $parent $zombie; do
		mkdir -p /out/proc/$pid
		for f in stat status cmdline cgroup; do cat /proc/$pid/$f > /out/proc/$pid/$f 2>/dev/null || true; done
	done
	for f in stat meminfo loadavg uptime; do cat /proc/$f > /out/proc/$f; done
	mkdir -p /out/etc && cp /etc/passwd /out/etc/passwd
	echo "normal=$normal other=$other odd=$odd parent=$parent zombie=$zombie" > /out/pids.txt
'
docker run --rm --pid=host -v "$PWD/$out:/out" "$image" bash -c '
	set -eu
	n=0
	worker=$(grep -l "^Name:.kworker" /proc/[0-9]*/status | head -1 | cut -d/ -f3)
	threaded=$(grep -l "^Name:.containerd$" /proc/[0-9]*/status | head -1 | cut -d/ -f3)
	for pid in 2 $worker $threaded; do
		n=$((n + 1))
		fake=$((9000 + n))
		mkdir -p /out/proc/$fake
		for f in stat status cmdline cgroup; do cat /proc/$pid/$f > /out/proc/$fake/$f 2>/dev/null || true; done
		# The PID inside the files: the fake one.
		sed -i "1s/^$pid /$fake /" /out/proc/$fake/stat
		sed -i "s/^Pid:\t$pid$/Pid:\t$fake/" /out/proc/$fake/status
		echo "host $pid -> $fake" >> /out/pids.txt
	done
'
cat "$out/pids.txt"
