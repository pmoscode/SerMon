#!/bin/sh
# Captures the stub_status page of nginx (M5-11) from the official images of several versions,
# after a few requests and with one client holding its connection open, so every counter is set.
# Usage: agent/fixtures/modules/nginx/capture.sh [version …]  (Docker)
set -eu
cd "$(dirname "$0")"
versions=${*:-1.22 1.24 1.26 1.28 1.29}
conf='server { listen 8080; location = /nginx_status { stub_status; } location / { return 200 "ok\n"; } }'

for version in $versions; do
	container="capture-nginx-$version"
	docker rm -f "$container" >/dev/null 2>&1 || true
	docker run -d --name "$container" "nginx:$version-alpine" >/dev/null
	docker exec "$container" sh -c "printf '%s\n' '$conf' >/etc/nginx/conf.d/status.conf && nginx -s reload"
	sleep 1
	docker exec "$container" sh -c '
		for i in 1 2 3 4 5 6 7 8 9 10; do wget -q -O /dev/null http://127.0.0.1:8080/; done
		((printf "GET / HTTP/1.1\r\nHost: x\r\n\r\n"; sleep 4) | nc 127.0.0.1 8080 >/dev/null) &
		((printf "GET / HTTP/1.1\r\nHost: x\r\n"; sleep 4) | nc 127.0.0.1 8080 >/dev/null) &
		sleep 1'
	mkdir -p "$version"
	docker exec "$container" wget -q -O - http://127.0.0.1:8080/nginx_status >"$version/stub_status.txt"
	docker exec "$container" nginx -v 2>"$version/version.txt" || true
	docker rm -f "$container" >/dev/null
	echo "✓ $version → agent/fixtures/modules/nginx/$version/"
done
