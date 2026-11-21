# A host with systemd as PID 1, sshd and sudo, to capture real security log lines for the parser
# fixtures (capture.sh). Throwaway containers only: the passwords below are test values.
ARG BASE=debian:13
FROM ${BASE}
RUN set -eux; \
    if command -v apt-get >/dev/null; then \
      apt-get update -qq; \
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends systemd systemd-sysv openssh-server openssh-client sudo procps python3 bsdutils >/dev/null; \
    else \
      dnf install -y -q systemd openssh-server openssh-clients sudo procps-ng python3 util-linux >/dev/null; \
      # Fedora ships script(1) in its own package; RHEL keeps it in util-linux.
      dnf install -y -q util-linux-script >/dev/null 2>&1 || true; \
    fi; \
    mkdir -p /var/log/journal; \
    useradd -m alice; echo 'alice:correct-horse' | chpasswd; \
    useradd -m carol; echo 'carol:correct-horse' | chpasswd; \
    echo 'root:correct-horse' | chpasswd; \
    echo 'alice ALL=(ALL) ALL' > /etc/sudoers.d/alice
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
