# Agent-CA

Die interne Zertifizierungsstelle, die die Zertifikate der Agents und der Agent-API ausstellt – nur für Superadmins. Ihre Zwischen-CA gilt zwei Jahre und wird nicht von selbst erneuert: Vor dem Ablauf ersetzt eine Umstellung sie durch eine neue.

## Ablauf und Warnung

Oben steht, wann die erste vertraute CA abläuft. Ab 90 Tagen davor warnt SerMon alle Superadmins per Mail und im Band unter der Kopfzeile, ab 30 Tagen kritisch. Nach dem Ablauf lehnen Agents und Server einander ab, und jeder Host muss neu angemeldet werden.

## Umstellung

1. **Umstellung starten** (mit erneuter Bestätigung): Eine neue CA stellt ab sofort alle Zertifikate aus. Die Agents bekommen beim nächsten Verbinden beide Roots und erneuern ihr Zertifikat bei ihrer nächsten Rotation bei der neuen CA. Bestehende Verbindungen laufen weiter, neue Hosts melden sich ganz normal an.
2. **Warten:** Die vorige CA zeigt, wie viele gültige Zertifikate sie noch hat. Spätestens 30 Tage nach dem Start sind alle ersetzt oder abgelaufen; Hosts, die so lange offline sind, müssen danach neu angemeldet werden.
3. **Vorige CA stilllegen** (mit erneuter Bestätigung), sobald sie keine Zertifikate mehr hat. Ab dann kommt auch das Zertifikat der Agent-API von der neuen CA.

Starte die Umstellung also spätestens 30 Tage vor dem Ablauf, besser früher. Beides steht im Audit-Log der Instanz.

## Fingerprint

Der SHA-256 des Root-Zertifikats jeder CA. Mit ihm lässt sich prüfen, welcher CA ein Host vertraut.
