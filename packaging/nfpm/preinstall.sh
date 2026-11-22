#!/bin/sh
# Package scripts (M2-21): the system user sermon, as install.sh creates it (ADR 0012). It stays
# after removal, as distributions do with system users.
set -eu
id sermon >/dev/null 2>&1 && exit 0
if command -v useradd >/dev/null 2>&1; then
	useradd --system --home-dir /var/lib/sermon-agent --no-create-home --shell /usr/sbin/nologin sermon
else
	addgroup -S sermon 2>/dev/null || true
	adduser -S -D -H -h /var/lib/sermon-agent -s /sbin/nologin -G sermon sermon
fi
# Read access to logs (KONZEPT §4.1); only groups that exist here, never wheel.
for group in systemd-journal adm; do
	if getent group "$group" >/dev/null 2>&1; then
		if command -v usermod >/dev/null 2>&1; then usermod -a -G "$group" sermon; else addgroup sermon "$group"; fi
	fi
done
