# Alerts

Die aktiven Alerts der Nodes, die du sehen darfst: was feuert, was auf seine Dauer wartet und was gerade endete – das Dringendste zuerst.

## Die Seite lesen

- **Kritisch** und **Warnung** feuern und benachrichtigen die Kanäle. **Ausstehend** wartet, bis die Bedingung lange genug gilt (die Dauer der Regel). **Behoben** endete gerade und verschwindet aus der Liste, sobald es ruhig bleibt.
- **Stumm** heißt: Eine Silence oder ein Wartungsfenster hält die Benachrichtigungen zurück; der Alert selbst läuft weiter und bleibt hier sichtbar.
- **Flattert** heißt: Der Alert wechselt zu oft; seine Benachrichtigungen werden gedrosselt, bis er wieder stabil ist.
- Werte der Hosts (Hostnamen, User-Namen, Adressen) erscheinen als Text. User und Adresse von Security-Alerts sehen nur Mitglieder, die die Security-Events sehen dürfen.
- **Verlauf** zeigt die Änderungen eines Alerts, die neueste zuerst; er bleibt ein Jahr gespeichert.
- **Charts um den Zeitpunkt** öffnet die Node-Seite eine Stunde vor bis eine Stunde nach dem Auslösen, im Bereich der Regel (CPU, Speicher, Datenträger, Netzwerk, Security); **Logs** zeigt die Logs des Nodes um den Zeitpunkt.

## Quittieren

Quittieren sagt „jemand kümmert sich“: Erinnerungen und Eskalation des Alerts stoppen, bis er endet oder kritisch wird, und die Kanäle erfahren, wer quittiert hat. Quittieren dürfen Operatoren der Gruppe des Nodes; bei Security-Alerts fragt SerMon noch einmal nach dem Passwort, weil Quittieren ihre Eskalation stoppt.

## Silences

Eine Silence schaltet die Benachrichtigungen passender Alerts bis zu 30 Tage stumm: Gruppen, einzelne Nodes dieser Gruppen, Regel-Muster (`security.*`), Labels oder Stufen – sie muss Gruppen, Regeln oder Labels nennen, damit nichts aus Versehen stumm wird, und braucht einen Kommentar. **Stummschalten** an einem Alert öffnet eine Silence für seine Regel auf seinem Node. Die Alerts bleiben sichtbar; was beim Ende noch feuert, kommt einmal nach. Operatoren schalten ihre eigenen Gruppen stumm und bestätigen mit dem Passwort; eine Silence vorzeitig zu beenden braucht kein Passwort.

## Regeln

Die Regeln bestimmen, wann ein Alert feuert. Jedes Mitglied kann sie mit Hilfetext und YAML lesen. Admins schalten Regeln ab, passen sie an, fügen eigene hinzu oder importieren mehrere auf einmal:

- Regeln sind typisierte YAML-Dokumente (threshold, rate, state, absence, prediction, event) – keine freien Abfragen. Der Editor prüft jedes Dokument; **Prüfen** zeigt Fehler mit Dokument und Zeile sowie die Änderungen als Diff, ohne etwas zu speichern. **Speichern** speichert alle Dokumente oder keines.
- Eine angepasste Standardregel lässt sich mit der mitgelieferten vergleichen und zurücksetzen; eine eigene Regel lässt sich löschen.
- **Exportieren** liefert die angepassten und eigenen Regeln (oder alle) als eine YAML-Datei, z. B. für eine andere Organisation. Ob eine Regel abgeschaltet ist, steht nicht in der Datei.
- Änderungen gelten ab der nächsten Auswertung (innerhalb von 10 Sekunden) und fragen noch einmal nach dem Passwort.

## Statistik

Der Reiter **Statistik** zeigt für die letzten 7 oder 30 Tage, wie oft Alerts ausgelöst haben, die lautesten Regeln und wie lange es im Mittel bis zur Quittung (MTTA) und bis zur Behebung (MTTR) dauerte – mehr unter [Alert-Statistik](help:concepts/alert-statistics).

## Typische nächste Schritte

1. Den Node öffnen und die Charts um den Zeitpunkt des Alerts ansehen.
2. Die Ursache beheben oder den Alert quittieren, damit niemand sonst geweckt wird.
3. Für geplante Arbeiten die Alerts mit einer Silence oder einem Wartungsfenster stummschalten.
