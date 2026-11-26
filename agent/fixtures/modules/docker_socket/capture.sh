#!/bin/sh
# Captures the read-only answers of the Docker API (M5-17, M5-21) from a Docker in Docker: the
# list of all containers and the inspection of each – a healthy and an unhealthy one with health
# checks, one that keeps restarting, an exited one, one without a health check – and the logs of a
# container writing to stdout and stderr (multiplexed) and of one with a TTY (raw). Only GET, as
# the module asks. Host-specific values (addresses, the daemon's paths) are documentation values.
# Usage: agent/fixtures/modules/docker_socket/capture.sh [docker:tag]  (Docker, --privileged)
set -eu
cd "$(dirname "$0")"
image=${1:-docker:28.3-dind}
name=$(printf '%s' "$image" | sed 's/:/-/; s/-dind$//')
c=capture-docker-api
docker rm -f "$c" >/dev/null 2>&1 || true
docker run -d --privileged --name "$c" -e DOCKER_TLS_CERTDIR= "$image" >/dev/null
tries=0
until docker exec "$c" docker info >/dev/null 2>&1; do
	tries=$((tries + 1))
	[ "$tries" -lt 60 ] || { docker logs "$c" | tail -20 >&2; exit 1; }
	sleep 1
done
docker exec "$c" sh -c '
	apk add -q curl >/dev/null
	docker run -d --name web --health-cmd "true" --health-interval 1s alpine:3.22 sleep 600 >/dev/null
	docker run -d --name sick --health-cmd "false" --health-interval 1s --health-retries 1 alpine:3.22 sleep 600 >/dev/null
	docker run -d --name flappy --restart always alpine:3.22 sh -c "sleep 1; exit 2" >/dev/null
	docker run --name done alpine:3.22 true
	docker run -d --name plain alpine:3.22 sleep 600 >/dev/null
	docker run -d --name talker alpine:3.22 sh -c "echo starting up; echo \"warning: low disk\" >&2; printf \"no newline\"; sleep 1; echo; echo ready" >/dev/null
	docker run -d -t --name tty alpine:3.22 sh -c "echo tty line one; echo tty line two >&2; sleep 600" >/dev/null
	sleep 12'
mkdir -p "$name"
api() { docker exec "$c" curl -s --unix-socket /var/run/docker.sock "http://localhost$1"; }
api "/containers/json?all=1" >"$name/containers.json"
for id in $(python3 -c 'import json,sys; [print(c["Id"]) for c in json.load(open(sys.argv[1]))]' "$name/containers.json"); do
	api "/containers/$id/json" >"$name/inspect-$(printf '%s' "$id" | cut -c1-12).json"
done
api "/version" >"$name/version.json"
for log in talker tty; do
	api "/containers/$log/logs?stdout=1&stderr=1&timestamps=1&tail=100" >"$name/logs-$log.bin"
done
docker rm -f "$c" >/dev/null
sed -i.bak -E 's/(172|10)\.[0-9]+\.[0-9]+\.([0-9]+)/192.0.2.\2/g' "$name"/*.json && rm "$name"/*.bak
# The talker exits after its lines; the list then says so.
echo "✓ $image → agent/fixtures/modules/docker_socket/$name/"
