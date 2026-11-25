#!/bin/sh
# Root helper package (M4-04): stop and disable the helper on removal, not on an upgrade
# (deb: upgrade, rpm: 1; apk runs this script on removal only). A run in progress is a unit or a
# process group of its own and finishes.
set -eu
case "${1:-}" in upgrade | failed-upgrade | 1) exit 0 ;; esac
if [ -d /run/systemd/system ]; then
	systemctl disable --now sermon-helper.socket sermon-helper.service >/dev/null 2>&1 || true
elif command -v rc-service >/dev/null 2>&1; then
	rc-service sermon-helper stop >/dev/null 2>&1 || true
	rc-update del sermon-helper default >/dev/null 2>&1 || true
fi
