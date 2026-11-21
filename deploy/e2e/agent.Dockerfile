# A Linux host with systemd as PID 1 for the agent: install.sh, the systemd unit and enroll run
# exactly as on a real server (M1-33). Debian and Ubuntu with apt, the RHEL family and Fedora
# with dnf (M2-19). Needs --privileged and the host cgroup namespace.
ARG BASE=debian:13
FROM ${BASE}
RUN set -eux; \
    if command -v apt-get >/dev/null; then \
      apt-get update -qq; \
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        systemd systemd-sysv curl ca-certificates openssl sudo >/dev/null; \
      rm -rf /var/lib/apt/lists/*; \
    else \
      dnf install -y -q systemd ca-certificates openssl sudo shadow-utils util-linux procps-ng >/dev/null; \
      # The RHEL images bring curl-minimal; installing curl over it would conflict.
      command -v curl >/dev/null || dnf install -y -q curl >/dev/null; \
      dnf clean all >/dev/null; \
    fi; \
    systemctl mask getty@.service console-getty.service systemd-logind.service >/dev/null 2>&1 || true
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
