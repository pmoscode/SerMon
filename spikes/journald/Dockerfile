# Container with systemd as PID 1 (journald, sshd, sudo) for the journalctl spike (M0-10).
ARG BASE=debian:13
FROM ${BASE}
ARG BASE
RUN set -eux; \
    if command -v apt-get >/dev/null; then \
      apt-get update -qq; \
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends systemd systemd-sysv openssh-server openssh-client sudo procps python3 >/dev/null; \
    else \
      dnf install -y -q systemd openssh-server openssh-clients sudo procps-ng python3 util-linux >/dev/null; \
    fi; \
    mkdir -p /var/log/journal; \
    useradd -m alice; echo 'alice:correct-horse' | chpasswd; \
    echo 'alice ALL=(ALL) ALL' > /etc/sudoers.d/alice
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
