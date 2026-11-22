#!/bin/sh
# Package scripts (M2-21): state directory, rights of the configuration, a machine-id where there is
# none (Alpine), and a restart of a running agent after an upgrade. A fresh agent is not started:
# it has no server and no identity yet.
set -eu
mkdir -p /var/lib/sermon-agent
chown sermon:sermon /var/lib/sermon-agent
chmod 0750 /var/lib/sermon-agent
chown root:sermon /etc/sermon /etc/sermon/agent.toml
chmod 0750 /etc/sermon
chmod 0640 /etc/sermon/agent.toml
if [ ! -s /etc/machine-id ] && [ ! -s /var/lib/dbus/machine-id ]; then
	od -An -N16 -tx1 /dev/urandom | tr -d ' \n' >/etc/machine-id
	echo >>/etc/machine-id
	chmod 0444 /etc/machine-id
fi
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	systemctl try-restart sermon-agent.service
elif command -v rc-service >/dev/null 2>&1; then
	rc-service --ifstarted sermon-agent restart >/dev/null 2>&1 || true
fi
if [ ! -e /var/lib/sermon-agent/identity ]; then
	echo "sermon-agent: set the server in /etc/sermon/agent.toml, then enroll and start (see the file)"
fi
