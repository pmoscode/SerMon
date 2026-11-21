# Alerts

Die aktiven Alerts der Nodes, die du sehen darfst: was feuert, was auf seine Dauer wartet und was gerade endete – das Dringendste zuerst.

## Die Seite lesen

- **Kritisch** und **Warnung** feuern und benachrichtigen die Kanäle. **Ausstehend** wartet, bis die Bedingung lange genug gilt (die Dauer der Regel). **Behoben** endete gerade und verschwindet aus der Liste, sobald es ruhig bleibt.
- **Stumm** heißt: Eine Silence oder ein Wartungsfenster hält die Benachrichtigungen zurück; der Alert selbst läuft weiter und bleibt hier sichtbar.
- **Flattert** heißt: Der Alert wechselt zu oft; seine Benachrichtigungen werden gedrosselt, bis er wieder stabil ist.
- Werte der Hosts (Hostnamen, User-Namen, Adressen) erscheinen als Text. User und Adresse von Security-Alerts sehen nur Mitglieder, die die Security-Events sehen dürfen.
- **Verlauf** zeigt die Änderungen eines Alerts, die neueste zuerst; er bleibt ein Jahr gespeichert.

## Quittieren

Quittieren sagt „jemand kümmert sich“: Erinnerungen und Eskalation des Alerts stoppen, bis er endet oder kritisch wird, und die Kanäle erfahren, wer quittiert hat. Quittieren dürfen Operatoren der Gruppe des Nodes; bei Security-Alerts fragt SerMon noch einmal nach dem Passwort, weil Quittieren ihre Eskalation stoppt.

## Typische nächste Schritte

1. Den Node öffnen und die Charts um den Zeitpunkt des Alerts ansehen.
2. Die Ursache beheben oder den Alert quittieren, damit niemand sonst geweckt wird.
3. Für geplante Arbeiten die Alerts mit einer Silence oder einem Wartungsfenster stummschalten.
