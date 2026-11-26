# Updates

Was die Nodes aktualisieren könnten, die letzten Rollouts und die Update-Policies der Gruppen.

## Nodes

Die Knöpfe oben filtern nach dem Stand: Security-Updates, andere Updates, Neustart nötig, aktuell, nicht verfügbar
(ohne Root-Helper oder ein Fehler beim Auflisten) und noch kein Bericht. **Ältestes Security-Update** sagt, seit wann
das älteste offene Security-Update gemeldet wird. Ein Klick auf den Node öffnet seinen Reiter **Updates** mit den
Paketen, den Läufen und „Jetzt aktualisieren“.

## Rollouts

Ein Rollout installiert die Updates einer Gruppe – nach ihrer Policy in einem Fenster oder von Hand. Jeder Node geht
durch „wartet“, „aktualisiert“, „startet neu“ und „Health-Check“ bis „fertig“; sonst steht dort, warum er
fehlgeschlagen ist oder übersprungen wurde (offline, ohne Root-Helper, vom Host verboten, Fensterende).

**Abbrechen** stoppt einen laufenden Rollout (Recht „Updates ausführen“ auf der Gruppe, mit erneuter Anmeldung):
wartende Nodes werden übersprungen, Läufe, die der Node noch nicht begonnen hat, zurückgezogen; Nodes, die gerade
aktualisieren, laufen ohne Neustart zu Ende.

## Updates pausieren

Admins pausieren hier alle Updates der Organisation, etwa bei einem Verdacht auf ein übernommenes Konto: Kein Rollout
startet mehr einen Node, wartende Läufe gehen nicht an die Nodes, Updates, Neustarts und Agent-Updates von Hand werden
abgelehnt. Was schon läuft, geht zu Ende. Der Hinweis oben zeigt allen, seit wann pausiert ist; **Updates fortsetzen**
hebt es auf. Beides steht im Audit-Log.

## Update-Policies

Jede Gruppe hat eine Policy; ohne eigene meldet sie nur. Admins ändern sie hier oder als YAML, siehe
[Update-Policy](help:concepts/update-policy).
