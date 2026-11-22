#!/bin/sh
# Package scripts (M2-21): stop and disable the agent on removal, not on an upgrade
# (deb: upgrade, rpm: 1; apk runs this script on removal only).
set -eu
case "${1:-}" in upgrade | failed-upgrade | 1) exit 0 ;; esac
if [ -d /run/systemd/system ]; then
	systemctl disable --now sermon-agent.service >/dev/null 2>&1 || true
elif command -v rc-service >/dev/null 2>&1; then
	rc-service sermon-agent stop >/dev/null 2>&1 || true
	rc-update del sermon-agent default >/dev/null 2>&1 || true
fi
