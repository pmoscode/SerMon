# Unverified source

The alert is based on lines of a log file any local user can write to; the message could be forged.

## Why

On systems with journald every line carries the user ID and the privileges of its sender, filled in by the system. SerMon reads SSH, sudo and account messages only if root or a privileged process wrote them.

Without journald (for example on Alpine) the agent reads `/var/log/messages`, `/var/log/auth.log` or `/var/log/secure`. They do not say who wrote a line: with `logger -t sshd …` any user can create a line that looks like a real sshd message. SerMon still reads it, but marks it.

## Check

- Look on the host whether the logins really happened, e.g. with `last` or in the logs of sshd.
- With many forged lines: who has an account on the host, and who ran `logger`?

## Verify with rsyslog

rsyslog knows the sender of a line (the kernel passes it over the socket). With this file as `/etc/rsyslog.d/sermon.conf` rsyslog writes a file in which the user ID comes before the text:

```
module(load="imuxsock" SysSock.Annotate="on" SysSock.ParseTrusted="on")
template(name="SermonTrusted" type="string" string="%timegenerated:::date-rfc3339% %inputname% uid=%$!uid% %syslogtag%%msg%\n")
auth,authpriv.*;kern.* action(type="omfile" file="/var/log/sermon/security.log" template="SermonTrusted" fileCreateMode="0640" fileGroup="sermon" dirCreateMode="0750" dirGroup="sermon")
```

Then restart rsyslog and the agent. The agent takes this file before all others. SSH and account messages from root then count as verified, forged ones are dropped.

**sudo stays unverified:** sudo reports with the user ID of the calling user, just like a forgery. Only journald tells them apart (by the privileges of the process).
