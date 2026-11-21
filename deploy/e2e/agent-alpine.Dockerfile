# An Alpine host with OpenRC for the agent (M2-20): busybox init starts OpenRC, OpenRC starts
# busybox syslogd and, after install.sh, the agent. No journald: the agent reads /var/log/messages.
# sudo only because the one-liner of the UI uses it; install.sh needs runuser (util-linux).
# Needs --privileged like the systemd hosts.
ARG BASE=alpine:3.22
FROM ${BASE}
RUN set -eux; \
    apk add --no-cache -q openrc busybox-openrc curl ca-certificates openssl sudo runuser; \
    # Containers: no hardware clock, no consoles, no kernel modules.
    sed -i 's/^#\?rc_sys=.*/rc_sys="docker"/' /etc/rc.conf; \
    sed -i '/getty/d' /etc/inittab; \
    rc-update add syslog boot
# busybox init powers off on SIGUSR2.
STOPSIGNAL SIGUSR2
CMD ["/sbin/init"]
