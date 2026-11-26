# Deaktivieren und löschen

Ein Node, der nicht mehr überwacht werden soll, wird deaktiviert oder gelöscht; beides können Admins seiner Gruppe nach einer frischen Bestätigung, und beides steht im Audit-Log.

## Deaktivieren

Alle Zertifikate des Nodes werden widerrufen, seine Verbindung endet sofort, und der Server lehnt ihn ab. Metriken, Events und Logs bleiben sichtbar; Alerts ruhen, offene Alerts enden, und es gibt keinen Alert „Node offline“. In der Übersicht steht er als „Deaktiviert“.

Zurück kommt der Node über **Übernahme erlauben** (Reiter Identität, Operator-Rechte) und eine neue Installation des Agents mit einem Token seiner Gruppe: Er behält ID und Historie.

## Löschen

Zum Bestätigen wird der Hostname eingegeben. Der Node wird sofort deaktiviert und nach der Frist – Standard 7 Tage, eingestellt vom Betreiber – mit seinen Metriken, Anomalien, Security-Events, Alerts und Logs gelöscht. Bis dahin hebt **Löschen abbrechen** das Löschen auf; der Node bleibt deaktiviert. Eine Übernahme ist während der Frist nicht möglich.

Was bleibt: die Einträge im Audit-Log, das Protokoll der Benachrichtigungen bis zu seiner eigenen Aufbewahrung und Silences, die den Node nennen. Bis zum Löschen zählt der Node zum Kontingent der Organisation und belegt seine `machine-id`.
