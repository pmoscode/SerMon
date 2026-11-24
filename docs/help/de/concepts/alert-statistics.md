# Alert-Statistik

Wie laut die Alerts der letzten 7 oder 30 Tage waren und wie schnell reagiert wurde – auf den Nodes, die du sehen darfst.

## Was gezählt wird

- Eine **Auslösung** beginnt, wenn ein Alert von einem ruhigen Zustand (OK, ausstehend, behoben) auf Warnung oder kritisch wechselt. Der Wechsel von Warnung auf kritisch ist keine neue Auslösung.
- Gezählt werden Auslösungen, die im Zeitraum beginnen. Eine, die vorher begann, fehlt – auch ihre Quittung und ihr Ende.
- **Noch aktiv** feuern weiter; **ohne Quittung** hat niemand quittiert, auch wenn sie inzwischen behoben sind.
- **Davon flatternd** sind Auslösungen, während der Alert flatterte; eine Regel, die oft flattert, ist meist zu scharf eingestellt.

## Lauteste Regeln

Die zehn Regeln mit den meisten Auslösungen, mit der Zahl der Nodes. Eine Regel, die oft auslöst und schnell von selbst behoben ist, braucht eher eine längere Dauer (`for`) oder eine andere Schwelle als mehr Aufmerksamkeit.
