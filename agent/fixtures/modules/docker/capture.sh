#!/bin/sh
# Captures what the module docker reads from cgroup v2 (M5-16): per container cgroup cpu.stat,
# memory.{current,max,stat,events}, io.stat, pids.current and cgroup.procs, and of its first
# process /proc/<pid>/{comm,net/dev}; twice, 5 s apart (t0, t1), so the rates can be tested.
#   docker/: the Docker host's own tree (Docker Desktop: cgroupfs driver, docker/<id>)
#   podman/: podman in a privileged container (cgroupfs manager, libpod_parent/libpod-<id>); its
#            containers share the network of that container (netns=host of quay.io/podman/stable)
# proc/net/dev is the host's (the agent's) network namespace.
# The IDs are replaced by readable ones (d0c1…, 90d1…, distinct in their first 12 digits); the layouts of the systemd drivers
# (system.slice/docker-<id>.scope, machine.slice/libpod-<id>.scope) are derived from these in
# the tests' fixture builder, with the same files.
# Usage: agent/fixtures/modules/docker/capture.sh  (Docker)
set -eu
cd "$(dirname "$0")"

# copy CGROUP_ROOT OUT DIR… : the files of each container cgroup DIR (relative to CGROUP_ROOT);
# a script for sh -c in the capturing containers, so it expands there.
# shellcheck disable=SC2016
copy='
cg=$1 out=$2
shift 2
mkdir -p "$out/sys/fs/cgroup" "$out/proc"
cp "$cg/cgroup.controllers" "$out/sys/fs/cgroup/"
grep "^cpu[0-9]" /proc/stat >"$out/proc/stat"
mkdir -p "$out/proc/net"
cat /proc/net/dev >"$out/proc/net/dev"
for dir; do
	mkdir -p "$out/sys/fs/cgroup/$dir"
	for f in cgroup.procs cpu.stat memory.current memory.max memory.stat memory.events io.stat pids.current; do
		cat "$cg/$dir/$f" >"$out/sys/fs/cgroup/$dir/$f" 2>/dev/null || true
	done
	pid=$(head -n 1 "$cg/$dir/cgroup.procs")
	[ -n "$pid" ] || continue
	mkdir -p "$out/proc/$pid/net"
	cat "/proc/$pid/comm" >"$out/proc/$pid/comm"
	cat "/proc/$pid/net/dev" >"$out/proc/$pid/net/dev"
done
'

# rename OUT PREFIX ID…: the IDs become PREFIX, a counter (below 10) and zeros, 64 hex digits.
rename() {
	out=$1 prefix=$2
	shift 2
	n=0
	for id; do
		n=$((n + 1))
		new=$(printf '%s%d%060d' "$prefix" "$n" 0)
		find "$out" -depth -name "*$id*" | while IFS= read -r path; do
			mv "$path" "$(dirname "$path")/$(basename "$path" | sed "s/$id/$new/")"
		done
	done
}

rm -rf docker podman

# Docker: a web server, a client fetching from it and a container writing to disk.
docker network create capture-docker >/dev/null
docker run -d --name capture-web --network capture-docker busybox sh -c 'mkdir -p /www && head -c 200000 /dev/urandom >/www/blob && exec httpd -f -p 8080 -h /www' >/dev/null
docker run -d --name capture-client --network capture-docker alpine sh -c 'while :; do wget -q -O /dev/null http://capture-web:8080/blob; done' >/dev/null
docker run -d --name capture-disk --memory 256m alpine sh -c 'while :; do dd if=/dev/zero of=/tmp/x bs=1M count=20 2>/dev/null; sync; rm /tmp/x; done' >/dev/null
sleep 3
ids=$(docker inspect -f '{{.Id}}' capture-web capture-client capture-disk)
dirs=$(for id in $ids; do printf 'docker/%s ' "$id"; done)
for t in t0 t1; do
	# shellcheck disable=SC2086 # the directories are words
	docker run --rm --pid=host --network=host --cgroupns=host -v /sys/fs/cgroup:/cg:ro -v "$PWD:/out" alpine \
		sh -c "$copy" copy /cg "/out/docker/$t" $dirs
	[ "$t" = t1 ] || sleep 5
done
docker rm -f capture-web capture-client capture-disk >/dev/null
docker network rm capture-docker >/dev/null
# shellcheck disable=SC2086
rename docker d0c $ids

# Podman (cgroupfs manager): a busy loop and a sleeping one.
docker run --rm --privileged --platform linux/arm64 -v "$PWD:/out" quay.io/podman/stable sh -c "
	podman --storage-driver=vfs run -d -q --cgroups=enabled --name busy docker.io/library/alpine sh -c 'while :; do :; done' >/dev/null
	podman --storage-driver=vfs run -d -q --cgroups=enabled --name idle docker.io/library/alpine sleep 600 >/dev/null
	sleep 3
	ids=\$(podman --storage-driver=vfs inspect -f '{{.Id}}' busy idle)
	dirs=\$(for id in \$ids; do printf 'libpod_parent/libpod-%s ' \"\$id\"; done)
	sh -c '$copy' copy /sys/fs/cgroup /out/podman/t0 \$dirs
	sleep 5
	sh -c '$copy' copy /sys/fs/cgroup /out/podman/t1 \$dirs
	echo \$ids >/out/podman/ids
"
# shellcheck disable=SC2046
rename podman 90d $(cat podman/ids)
rm podman/ids
echo "captured: docker/ podman/"
