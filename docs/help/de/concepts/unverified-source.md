# Nicht verifizierte Quelle

Der Alert beruht auf Zeilen einer Logdatei, die jeder lokale Benutzer schreiben kann; die Meldung könnte gefälscht sein.

## Warum

Auf Systemen mit journald trägt jede Zeile die Benutzer-ID und die Rechte des Absenders, eingetragen vom System. SerMon wertet SSH-, sudo- und Konto-Meldungen nur aus, wenn root oder ein Prozess mit Sonderrechten sie geschrieben hat.

Ohne journald (etwa auf Alpine) liest der Agent `/var/log/messages`, `/var/log/auth.log` oder `/var/log/secure`. Dort steht nicht, wer eine Zeile geschrieben hat: Mit `logger -t sshd …` kann jeder Benutzer eine Zeile erzeugen, die wie eine echte sshd-Meldung aussieht. SerMon wertet sie trotzdem aus, markiert sie aber.

## Prüfen

- Auf dem Host nachsehen, ob die Anmeldungen wirklich stattfanden, z. B. mit `last` oder in den Logs von sshd.
- Bei vielen gefälschten Zeilen: Wer hat auf dem Host ein Konto, und wer hat `logger` aufgerufen?

## Verifizieren mit rsyslog

rsyslog kennt den Absender einer Zeile (vom Kernel über den Socket mitgeteilt). Mit dieser Datei unter `/etc/rsyslog.d/sermon.conf` schreibt rsyslog eine Datei, in der die Benutzer-ID vor dem Text steht:

```
module(load="imuxsock" SysSock.Annotate="on" SysSock.ParseTrusted="on")
template(name="SermonTrusted" type="string" string="%timegenerated:::date-rfc3339% %inputname% uid=%$!uid% %syslogtag%%msg%\n")
auth,authpriv.*;kern.* action(type="omfile" file="/var/log/sermon/security.log" template="SermonTrusted" fileCreateMode="0640" fileGroup="sermon" dirCreateMode="0750" dirGroup="sermon")
```

Danach rsyslog und den Agent neu starten. Der Agent nimmt diese Datei vor allen anderen. SSH- und Konto-Meldungen von root gelten dann als verifiziert, gefälschte werden verworfen.

**sudo bleibt nicht verifiziert:** sudo meldet sich mit der Benutzer-ID des aufrufenden Benutzers, genau wie eine Fälschung. Nur journald unterscheidet beide (über die Rechte des Prozesses).
