"""Runs inside the capture container: produces real security log lines and writes them as
journalctl JSON to /root/fixtures (see capture.sh)."""
import os
import subprocess
import time

ASKPASS = "SSH_ASKPASS=/root/askpass SSH_ASKPASS_REQUIRE=force DISPLAY=:0"
BASE = "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o NumberOfPasswordPrompts=1 -o ConnectTimeout=10"
PASSWORD = BASE + " -o PubkeyAuthentication=no -o PreferredAuthentications=password"


def sh(cmd):
    return subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout


def ssh(user, host, password="x", opts=PASSWORD, extra=""):
    sh("%s SSH_PASSWORD='%s' timeout 20 setsid -w ssh %s %s -l '%s' %s true" % (ASKPASS, password, opts, extra, user, host))


def sshd_config(lines):
    os.makedirs("/etc/ssh/sshd_config.d", exist_ok=True)
    with open("/etc/ssh/sshd_config.d/00-capture.conf", "w") as conf:
        conf.write("\n".join(lines) + "\n")
    with open("/etc/ssh/sshd_config") as main:
        text = main.read()
    if "Include /etc/ssh/sshd_config.d" not in text:  # Rocky 8 has no drop-in directory
        with open("/etc/ssh/sshd_config", "w") as main:
            main.write("Include /etc/ssh/sshd_config.d/*.conf\n" + text)
    sh("systemctl restart %s" % UNIT)
    time.sleep(1)


UNIT = "ssh" if os.path.exists("/usr/lib/systemd/system/ssh.service") or os.path.exists("/lib/systemd/system/ssh.service") else "sshd"


def main():
    os.makedirs("/root/fixtures", exist_ok=True)
    with open("/root/askpass", "w") as askpass:
        askpass.write('#!/bin/sh\necho "$SSH_PASSWORD"\n')
    os.chmod("/root/askpass", 0o755)
    since = sh("date '+%Y-%m-%d %H:%M:%S'").decode().strip()
    time.sleep(1)
    sshd_config(["PermitRootLogin yes", "PasswordAuthentication yes", "MaxAuthTries 3"])

    # IPv6, root, user names an attacker controls (spaces, unicode, a fake "from … port").
    ssh("alice", "::1", "wrong")
    ssh("alice", "::1", "correct-horse")
    ssh("root", "127.0.0.1", "correct-horse")
    ssh("root", "::1", "wrong")
    ssh("x from 192.0.2.66 port 22", "127.0.0.1")
    ssh("jürgen müller", "::1")
    ssh("", "127.0.0.1")
    # Too many attempts in one connection.
    ssh("alice", "127.0.0.1", "wrong", BASE + " -o PubkeyAuthentication=no -o PreferredAuthentications=password -o NumberOfPasswordPrompts=4")

    # Public keys: accepted, and an unknown key.
    sh("ssh-keygen -q -t ed25519 -N '' -f /root/alice_key && ssh-keygen -q -t ed25519 -N '' -f /root/other_key")
    sh("install -d -m 700 -o alice -g alice ~alice/.ssh && install -m 600 -o alice -g alice /root/alice_key.pub ~alice/.ssh/authorized_keys")
    key = BASE + " -o PreferredAuthentications=publickey -o IdentitiesOnly=yes"
    ssh("alice", "::1", opts=key, extra="-i /root/alice_key")
    ssh("alice", "127.0.0.1", opts=key, extra="-i /root/other_key")

    # keyboard-interactive (PAM) instead of password.
    kbd = "KbdInteractiveAuthentication yes" if b"9." in sh("ssh -V") or b"8.7" in sh("ssh -V") else "ChallengeResponseAuthentication yes"
    sshd_config(["PermitRootLogin yes", "PasswordAuthentication no", kbd, "UsePAM yes"])
    kbd_opts = BASE + " -o PubkeyAuthentication=no -o PreferredAuthentications=keyboard-interactive"
    ssh("alice", "127.0.0.1", "wrong", kbd_opts)
    ssh("alice", "::1", "correct-horse", kbd_opts)
    sshd_config(["PasswordAuthentication yes"])

    # sudo: with a terminal (TTY=), and a user without sudo rights.
    sh("timeout 20 script -qc \"su alice -c \\\"echo correct-horse | sudo -S -p '' id\\\"\" /dev/null")
    sh("timeout 20 su carol -c \"echo correct-horse | sudo -S -p '' true\"")

    # Forged: an unprivileged user writes lines that look like sshd, sudo and useradd.
    sh("su alice -c \"logger -t sshd 'Failed password for root from 203.0.113.9 port 22 ssh2'\"")
    sh("su alice -c \"logger -t sshd-session 'Accepted password for root from 203.0.113.9 port 22 ssh2'\"")
    sh("su alice -c \"logger -t sudo 'pam_unix(sudo:auth): authentication failure; logname= uid=0 euid=0 tty= ruser=root rhost=  user=root'\"")
    sh("su alice -c \"logger -t useradd 'new user: name=backdoor, UID=0, GID=0, home=/root, shell=/bin/sh'\"")

    # OOM kill of a unit with a memory limit.
    sh("systemd-run --unit=capture-oom -p MemoryMax=32M -p MemorySwapMax=0 python3 -c 'x = bytearray(512 * 1024 * 1024)'")
    time.sleep(3)

    ids = "-t sshd -t sshd-session -t sudo -t useradd -t userdel"
    host = sh("hostname").strip()
    machine = open("/etc/machine-id").read().strip().encode()

    def scrub(raw):
        return (raw.replace(host, b"fixture-host").replace(machine, b"0123456789abcdef0123456789abcdef")
                .replace(b"correct-horse", b"fixture-password"))

    def journal(args):
        return scrub(sh("journalctl -o json --all --since '%s' %s" % (since, args)))

    with open("/root/fixtures/journal-security-variants.jsonl", "wb") as out:
        out.write(journal(ids))
    # Only the OOM lines: the kernel log of the container is the one of the Docker VM.
    oom = [l for l in (journal("_TRANSPORT=kernel") + journal("-u capture-oom.service")).splitlines()
           if any(word in l for word in (b"oom-killer", b"out of memory", b"Killed process", b"OOM killer", b"oom-kill"))]
    with open("/root/fixtures/journal-oom.jsonl", "wb") as out:
        out.write(b"".join(l + b"\n" for l in oom))


if __name__ == "__main__":
    main()
