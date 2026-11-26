#!/bin/sh
# Captures `server-status?auto` of Apache httpd (M5-12) from the packages of the distributions:
# Debian with the event and the prefork MPM (mod_status is on there, for localhost), the RHEL
# family and Fedora with a <Location /server-status> of their own. A few requests first, so the
# counters are set; the host name of the first line is replaced.
# Usage: agent/fixtures/modules/apache/capture.sh [name …]  (Docker)
set -eu
cd "$(dirname "$0")"
names=${*:-debian-13-event debian-13-prefork rockylinux-9 almalinux-10 fedora-44}
location='<Location "/server-status">
    SetHandler server-status
    Require local
</Location>'

for name in $names; do
	container="capture-apache-$name"
	docker rm -f "$container" >/dev/null 2>&1 || true
	case $name in
	debian-*) image="debian:13" ;;
	rockylinux-9) image="rockylinux:9" ;;
	almalinux-10) image="almalinux:10" ;;
	fedora-44) image="fedora:44" ;;
	*) echo "unknown: $name" >&2; exit 2 ;;
	esac
	docker run -d --name "$container" --hostname fixture-host "$image" sleep 600 >/dev/null
	case $name in
	debian-*)
		docker exec "$container" sh -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq apache2 curl >/dev/null'
		if [ "$name" = debian-13-prefork ]; then
			docker exec "$container" sh -c 'a2dismod -q mpm_event >/dev/null && a2enmod -q mpm_prefork >/dev/null'
		fi
		docker exec "$container" apachectl start
		;;
	*)
		docker exec "$container" sh -c 'dnf install -y -q httpd >/dev/null'
		docker exec "$container" sh -c "printf '%s\n' '$location' >/etc/httpd/conf.d/status.conf && httpd -k start"
		;;
	esac
	sleep 2
	docker exec "$container" sh -c 'for i in 1 2 3 4 5 6 7 8 9 10; do curl -s -o /dev/null http://127.0.0.1/; done'
	mkdir -p "$name"
	docker exec "$container" curl -s "http://127.0.0.1/server-status?auto" >"$name/server-status.txt"
	sed -i.bak '1s/.*/fixture-host/' "$name/server-status.txt" && rm "$name/server-status.txt.bak"
	docker rm -f "$container" >/dev/null
	echo "✓ $name → agent/fixtures/modules/apache/$name/"
done
