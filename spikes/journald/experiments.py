#!/usr/bin/env python3
"""Spike M0-10: journalctl as a child process. Runs inside a systemd container (run.sh).

Prints one line per finding ("name: result") and writes fixtures to /root/fixtures.
Compatible with Python 3.6 (Rocky 8).
"""
import json
import os
import queue
import re
import signal
import subprocess
import threading
import time
import uuid

CURSOR = "/tmp/cursor"


def sh(cmd, check=False):
    p = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, universal_newlines=True)
    if check and p.returncode:
        raise RuntimeError("%s: %s" % (cmd, p.stdout))
    return p.stdout.strip()


def report(name, value):
    print("%s: %s" % (name, value), flush=True)


def log(tag, msg):
    subprocess.run(["logger", "-t", tag, msg], check=True)


def systemd_version():
    return int(re.search(r"systemd (\d+)", sh("journalctl --version")).group(1))


class Follower:
    """journalctl -o json -f as a child, lines collected by a thread."""

    def __init__(self, args, user=None):
        cmd = ["journalctl", "-o", "json", "-f"] + args
        if user:
            cmd = ["runuser", "-u", user, "--"] + cmd
        self.proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.lines = queue.Queue()
        self.started = time.time()
        threading.Thread(target=self._read, daemon=True).start()
        # journalctl needs a moment to open the journal and seek; entries written before that are
        # not "new" for -n 0.
        time.sleep(1.0)

    def _read(self):
        for raw in self.proc.stdout:
            self.lines.put((time.time(), raw))

    def entries(self, wait=1.5):
        out = []
        deadline = time.time() + wait
        while time.time() < deadline:
            try:
                _, raw = self.lines.get(timeout=0.1)
                out.append(json.loads(raw.decode("utf-8")))
            except queue.Empty:
                pass
        return out

    def stop(self, sig=signal.SIGTERM):
        self.proc.send_signal(sig)
        self.proc.wait(timeout=10)
        return self.proc.returncode


def markers(prefix, n):
    tag = "sermon-spike"
    run = uuid.uuid4().hex[:6]
    for i in range(n):
        log(tag, "%s-%s-%d" % (prefix, run, i))
    time.sleep(0.5)
    return ["%s-%s-%d" % (prefix, run, i) for i in range(n)]


def msgs(entries, prefix):
    return [e.get("MESSAGE") for e in entries if isinstance(e.get("MESSAGE"), str) and e["MESSAGE"].startswith(prefix)]


def format_checks():
    tag = "sermon-format"
    subprocess.run("printf 'binary \\377 value\\n' | systemd-cat -t %s" % tag, shell=True, check=True)
    # logger truncates to 1 KiB; systemd-cat passes the whole line (journald LineMax 48 KiB).
    subprocess.run("python3 -c 'print(\"x\" * 5000)' | systemd-cat -t %s" % tag, shell=True, check=True)
    subprocess.run(["logger", "-t", tag, "-p", "auth.warning", "priority test"], check=True)
    time.sleep(0.5)
    # Raw output as fixture, before the vacuum experiment removes the entries.
    os.makedirs("/root/fixtures", exist_ok=True)
    raw = subprocess.run(["journalctl", "-o", "json", "--all", "-t", tag], stdout=subprocess.PIPE).stdout
    host = sh("hostname").encode()
    with open("/root/fixtures/format.jsonl", "wb") as out:
        out.write(raw.replace(host, b"fixture-host").replace(open("/etc/machine-id").read().strip().encode(), b"0123456789abcdef0123456789abcdef"))
    plain = [json.loads(l) for l in sh("journalctl -o json -t %s" % tag).splitlines()]
    full = [json.loads(l) for l in sh("journalctl -o json --all -t %s" % tag).splitlines()]
    report("fields", " ".join(sorted(plain[-1].keys())))
    report("realtime type", type(plain[-1]["__REALTIME_TIMESTAMP"]).__name__)
    report("priority value", repr(plain[-1].get("PRIORITY")))
    binary = [e for e in plain if isinstance(e.get("MESSAGE"), list)]
    report("binary MESSAGE", "list of %d ints" % len(binary[0]["MESSAGE"]) if binary else "not a list")
    def big(entries):
        for e in entries:
            v = e.get("MESSAGE", "absent")
            if v is None:
                return "null"
            if isinstance(v, str) and v.startswith("xxxx"):
                return "string of %d" % len(v)
        return "absent"
    report("5000-byte MESSAGE without --all", big(plain))
    report("5000-byte MESSAGE with --all", big(full))
    report("__SEQNUM present", "__SEQNUM" in plain[-1])


def cursor_file_checks():
    if systemd_version() < 242:
        out = sh("journalctl -o json -n 1 --cursor-file=%s" % CURSOR)
        report("--cursor-file", "unsupported: " + out.splitlines()[-1][:80])
        return
    if os.path.exists(CURSOR):
        os.remove(CURSOR)
    f = Follower(["--cursor-file=" + CURSOR])
    first = f.entries(1.0)
    report("first start without cursor file", "%d old entries (tail)" % len(first))
    a = markers("cf-a", 5)
    got = msgs(f.entries(), "cf-a")
    rc = f.stop()
    report("follow sees new entries", "%d/5" % len(got))
    report("cursor file after SIGTERM", "written (rc %s)" % rc if os.path.exists(CURSOR) else "missing (rc %s)" % rc)

    b = markers("cf-b", 5)
    f = Follower(["--cursor-file=" + CURSOR])
    got = f.entries()
    report("restart after SIGTERM", "%d new, %d duplicates of the old run" % (len(msgs(got, "cf-b")), len(msgs(got, "cf-a"))))
    before = open(CURSOR).read() if os.path.exists(CURSOR) else ""
    c = markers("cf-c", 5)
    f.entries()
    f.stop(signal.SIGKILL)
    after = open(CURSOR).read() if os.path.exists(CURSOR) else ""
    report("cursor file after SIGKILL", "unchanged" if before == after else "updated")
    f = Follower(["--cursor-file=" + CURSOR])
    got = f.entries()
    f.stop()
    report("restart after SIGKILL", "%d entries delivered again" % len(msgs(got, "cf-c")))
    del a, b, c


def after_cursor_checks():
    f = Follower(["-n", "0"])
    m = markers("ac-a", 3)
    got = f.entries()
    f.stop()
    last = [e for e in got if e.get("MESSAGE") == m[-1]][0]["__CURSOR"]
    markers("ac-b", 4)
    f = Follower(["--after-cursor=" + last])
    got = f.entries()
    f.stop()
    report("--after-cursor restart", "%d new, %d duplicates" % (len(msgs(got, "ac-b")), len(msgs(got, "ac-a"))))
    return last


def rotation_checks():
    f = Follower(["-n", "0"])
    markers("rot-a", 3)
    sh("journalctl --rotate", check=True)
    markers("rot-b", 3)
    got = f.entries()
    report("follow across --rotate", "%d/6, journalctl alive: %s" % (len(msgs(got, "rot-")), f.proc.poll() is None))
    sh("systemctl restart systemd-journald", check=True)
    time.sleep(1)
    markers("rst", 3)
    got = f.entries(2.5)
    report("follow across journald restart", "%d/3, journalctl alive: %s" % (len(msgs(got, "rst")), f.proc.poll() is None))
    f.stop()


def vacuum_checks():
    f = Follower(["-n", "0"])
    m = markers("vac-a", 2)
    got = f.entries()
    f.stop()
    last = [e for e in got if e.get("MESSAGE") == m[-1]][0]
    markers("vac-lost", 50)
    sh("journalctl --rotate && sleep 1 && journalctl --vacuum-time=1s", check=True)
    markers("vac-b", 2)
    f = Follower(["--after-cursor=" + last["__CURSOR"]])
    got = f.entries(2.0)
    f.stop()
    lost = msgs(got, "vac-lost")
    new = msgs(got, "vac-b")
    report("restart after vacuum (cursor deleted)", "rc ok, %d new, %d of 50 vacuumed entries returned" % (len(new), len(lost)))
    if got and "__SEQNUM" in got[0] and "__SEQNUM" in last:
        gap = int(got[0]["__SEQNUM"]) - int(last["__SEQNUM"]) - 1
        report("gap detectable via __SEQNUM", "yes, %d entries skipped" % gap)
    else:
        report("gap detectable via __SEQNUM", "no __SEQNUM; compare __REALTIME_TIMESTAMP only")


def latency_check():
    f = Follower(["-n", "0"])
    samples = []
    for i in range(5):
        probe = "latency-probe-%d" % i
        sent = time.time()
        log("sermon-latency", probe)
        deadline = time.time() + 5
        while time.time() < deadline:
            try:
                at, raw = f.lines.get(timeout=0.05)
                if probe.encode() in raw:
                    samples.append((at - sent) * 1000)
                    break
            except queue.Empty:
                pass
        time.sleep(0.3)
    f.stop()
    samples.sort()
    report("pipe latency (5 probes)", "median %.0f ms, max %.0f ms" % (samples[len(samples) // 2], samples[-1]) if samples else "not seen")


def proc_stats(pid):
    stat = open("/proc/%d/stat" % pid).read().split(")")[1].split()
    ticks = os.sysconf("SC_CLK_TCK")
    cpu = (int(stat[11]) + int(stat[12])) / ticks
    status = open("/proc/%d/status" % pid).read()
    hwm = int(re.search(r"VmHWM:\s+(\d+)", status).group(1))
    rss = int(re.search(r"VmRSS:\s+(\d+)", status).group(1))
    return cpu, rss, hwm


def load_check():
    f = Follower(["-n", "0"])
    time.sleep(1)
    cpu0, idle_rss, _ = proc_stats(f.proc.pid)
    n = 20000
    start = time.time()
    subprocess.run("for i in $(seq 1 %d); do echo \"load line $i with some text\"; done | systemd-cat -t sermon-load" % n, shell=True, check=True)
    got = 0
    deadline = time.time() + 60
    while got < n and time.time() < deadline:
        try:
            _, raw = f.lines.get(timeout=0.5)
            if b"sermon-load" in raw:
                got += 1
        except queue.Empty:
            pass
    elapsed = time.time() - start
    cpu1, rss, hwm = proc_stats(f.proc.pid)
    f.stop()
    report("idle RSS", "%d KiB" % idle_rss)
    report("load %d lines" % n, "%d read in %.1f s (%.0f lines/s), journalctl CPU %.2f s = %.0f µs/line, RSS %d KiB, peak %d KiB"
           % (got, elapsed, got / elapsed, cpu1 - cpu0, (cpu1 - cpu0) / max(got, 1) * 1e6, rss, hwm))


def permission_checks():
    sh("useradd --system --no-create-home sermon")
    f = Follower(["-n", "0"], user="sermon")
    markers("perm-a", 2)
    no_group = len(msgs(f.entries(), "perm-a"))
    f.stop()
    sh("usermod -a -G systemd-journal sermon", check=True)
    f = Follower(["-n", "0"], user="sermon")
    markers("perm-b", 2)
    group = len(msgs(f.entries(), "perm-b"))
    f.stop()
    report("user sermon without group", "%d/2 system entries" % no_group)
    report("user sermon in systemd-journal", "%d/2 system entries" % group)


def fixtures():
    os.makedirs("/root/fixtures", exist_ok=True)
    since = sh("date '+%Y-%m-%d %H:%M:%S'")
    time.sleep(1)
    unit = "ssh" if os.path.exists("/lib/systemd/system/ssh.service") or os.path.exists("/usr/lib/systemd/system/ssh.service") else "sshd"
    sh("systemctl start %s" % unit)
    time.sleep(1)
    with open("/root/askpass", "w") as a:
        a.write("#!/bin/sh\necho \"$SSH_PASSWORD\"\n")
    os.chmod("/root/askpass", 0o755)
    opts = "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o PubkeyAuthentication=no -o PreferredAuthentications=password,keyboard-interactive -o NumberOfPasswordPrompts=1"
    env = "SSH_ASKPASS=/root/askpass SSH_ASKPASS_REQUIRE=force DISPLAY=:0"
    for user, pw in (("alice", "wrong"), ("alice", "correct-horse"), ("mallory", "x")):
        sh("%s SSH_PASSWORD=%s timeout 20 setsid -w ssh %s %s@127.0.0.1 true" % (env, pw, opts, user))
    # sudo asks three times for a wrong password; -S with three wrong lines ends it, timeout guards.
    sh("timeout 20 su alice -c \"echo correct-horse | sudo -S -p '' true\"")
    sh("timeout 20 su alice -c \"printf 'wrong\\nwrong\\nwrong\\n' | sudo -S -p '' true\"")
    sh("useradd -m bob && usermod -a -G sudo bob 2>/dev/null || usermod -a -G wheel bob; userdel -r bob")
    time.sleep(1)
    ids = "-t sshd -t sshd-session -t sudo -t useradd -t usermod -t userdel -t groupadd -t su -t unix_chkpwd"
    host = sh("hostname").encode()
    machine = open("/etc/machine-id").read().strip().encode()

    def scrub(raw):
        # Container values only, but replace them with obvious fixture values; the test password
        # shows up in _CMDLINE of su (a case for the redaction, KONZEPT §10.3).
        return (raw.replace(host, b"fixture-host").replace(machine, b"0123456789abcdef0123456789abcdef")
                .replace(b"correct-horse", b"fixture-password"))

    def journal(args):
        # Raw bytes: old journalctl versions write invalid UTF-8 into JSON strings.
        return subprocess.run("journalctl -o json --all " + args, shell=True, stdout=subprocess.PIPE).stdout

    security = scrub(journal("--since '%s' %s" % (since, ids)))
    with open("/root/fixtures/security.jsonl", "wb") as out:
        out.write(security)
    report("fixtures", "%d security entries" % security.count(b"\n"))


def main():
    report("os", sh(". /etc/os-release; echo \"$PRETTY_NAME\""))
    report("journalctl", sh("journalctl --version").splitlines()[0])
    report("storage", "persistent" if os.listdir("/var/log/journal") else "volatile")
    for check in (format_checks, cursor_file_checks, after_cursor_checks, rotation_checks, vacuum_checks, latency_check, load_check, permission_checks, fixtures):
        try:
            check()
        except Exception as error:  # noqa: BLE001 - a spike reports and goes on
            report(check.__name__, "FAILED: %r" % error)


if __name__ == "__main__":
    main()
