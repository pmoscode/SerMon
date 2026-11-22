#!/bin/sh
# Package scripts (M2-21): on removal the agent's identity, buffer and positions go (as
# `sermon-agent uninstall`); the configuration and the user sermon stay. Not on an upgrade
# (deb: upgrade, failed-upgrade, abort-upgrade; rpm: 1).
set -eu
case "${1:-}" in upgrade | failed-upgrade | abort-upgrade | 1) exit 0 ;; esac
rm -rf /var/lib/sermon-agent
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
fi
