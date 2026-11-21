#!/bin/sh
# Captures the security log lines of Alpine (M2-20, ADR 0025) in both formats the agent reads:
# busybox syslogd (/var/log/messages) and the SerMon rsyslog template (rsyslog.conf below, the same
# as in docs/help/*/concepts/unverified-source.md). Real sshd, sudo and useradd lines, and lines an
# unprivileged user forges with logger. Writes agent/fixtures/logs/alpine-3.22/.
set -eu
image=${1:-alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f}
out=$(cd "$(dirname "$0")/.." && pwd)/alpine-3.22
mkdir -p "$out"

run() {
	docker run --rm -i --hostname fixture-host "$image" sh -s "$1" <<'INNER'
set -eu
mode=$1
apk add -q openssh sudo shadow rsyslog >/dev/null 2>&1
adduser -D alice >/dev/null 2>&1
echo "alice:correct-horse" | chpasswd >/dev/null 2>&1
adduser -D carol >/dev/null 2>&1
echo "carol:correct-horse" | chpasswd >/dev/null 2>&1
echo "alice ALL=(ALL) ALL" > /etc/sudoers.d/alice
adduser -D -H -s /sbin/nologin sermon >/dev/null 2>&1
if [ "$mode" = trusted ]; then
	mkdir -p /etc/rsyslog.d
	cat > /etc/rsyslog.conf <<'CONF'
module(load="imuxsock" SysSock.Annotate="on" SysSock.ParseTrusted="on")
template(name="SermonTrusted" type="string" string="%timegenerated:::date-rfc3339% %inputname% uid=%$!uid% %syslogtag%%msg%\n")
auth,authpriv.*;kern.* action(type="omfile" file="/var/log/sermon/security.log" template="SermonTrusted" fileCreateMode="0640" fileGroup="sermon" dirCreateMode="0750" dirGroup="sermon")
CONF
	rsyslogd
	log=/var/log/sermon/security.log
else
	syslogd -t
	log=/var/log/messages
fi
sleep 1
ssh-keygen -A >/dev/null
printf 'PermitRootLogin yes\nPasswordAuthentication yes\nKbdInteractiveAuthentication no\n' > /etc/ssh/sshd_config.d/00-capture.conf 2>/dev/null ||
	printf 'PermitRootLogin yes\nPasswordAuthentication yes\n' >> /etc/ssh/sshd_config
/usr/sbin/sshd
sleep 1
printf '#!/bin/sh\necho "$SSH_PASSWORD"\n' > /root/askpass && chmod 755 /root/askpass
try() { SSH_ASKPASS=/root/askpass SSH_ASKPASS_REQUIRE=force DISPLAY=:0 SSH_PASSWORD="$3" timeout 10 ssh -n -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no -o PreferredAuthentications=password -l "$1" "$2" true >/dev/null 2>&1 || true; }
try alice 127.0.0.1 wrong
try alice ::1 correct-horse
try nosuchuser 127.0.0.1 wrong
su alice -c "echo wrong | sudo -S -p '' true" >/dev/null 2>&1 || true
su carol -c "echo correct-horse | sudo -S -p '' true" >/dev/null 2>&1 || true
useradd -M bob
# Forged by an unprivileged user.
su alice -c "logger -p auth.info -t sshd 'Accepted password for root from 203.0.113.9 port 22 ssh2'"
su alice -c "logger -p auth.info -t useradd 'new user: name=backdoor, UID=0, GID=0, home=/root, shell=/bin/sh'"
sleep 1
# The agent's user must be able to read the file.
su sermon -s /bin/sh -c "test -r $log" || { echo "sermon cannot read $log" >&2; exit 1; }
cat "$log"
INNER
}
run syslog | grep -E 'sshd|sudo|useradd|kernel' > "$out/messages"
run trusted > "$out/security.log"
echo "✓ $out"
