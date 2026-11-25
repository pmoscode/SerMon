#!/bin/sh
# Root helper package (M4-04): the state directory, and on the first installation the socket
# (systemd) or the service (OpenRC). An upgrade restarts a running helper but never enables one
# the admin switched off – that is the host's veto (ADR 0035).
set -eu
mkdir -p /var/lib/sermon-helper
chmod 0700 /var/lib/sermon-helper
first=1
case "${1:-}" in
configure) [ -z "${2:-}" ] || first='' ;; # deb: the old version on an upgrade
2) first='' ;;                             # rpm: an upgrade
esac
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	if [ -n "$first" ]; then
		systemctl enable --now sermon-helper.socket
	else
		systemctl try-restart sermon-helper.service
	fi
elif command -v rc-service >/dev/null 2>&1 && [ -n "$first" ]; then
	rc-update add sermon-helper default >/dev/null 2>&1 || true
	rc-service sermon-helper start >/dev/null 2>&1 || true
fi
