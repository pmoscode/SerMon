# A Linux host with systemd as PID 1 for the agent: install.sh, the systemd unit and enroll run
# exactly as on a real server (M1-33). Needs --privileged and the host cgroup namespace.
ARG BASE=debian:13
FROM ${BASE}
RUN set -eux; \
    apt-get update -qq; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      systemd systemd-sysv curl ca-certificates openssl sudo >/dev/null; \
    rm -rf /var/lib/apt/lists/*; \
    systemctl mask getty@.service console-getty.service systemd-logind.service >/dev/null 2>&1 || true
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
