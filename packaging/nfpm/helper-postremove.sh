#!/bin/sh
# Root helper package (M4-04): on removal the results of runs and the socket go (OpenRC leaves
# the file of a helper ended by a signal); /etc/sermon/helper.toml stays
# (configuration). Not on an upgrade (deb: upgrade, failed-upgrade, abort-upgrade; rpm: 1).
set -eu
case "${1:-}" in upgrade | failed-upgrade | abort-upgrade | 1) exit 0 ;; esac
rm -rf /var/lib/sermon-helper
rm -f /run/sermon/helper.sock
rmdir /run/sermon 2>/dev/null || true
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
fi
