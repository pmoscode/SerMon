# Updates

Was der Node aktualisieren könnte, aus dem letzten Bericht seines Agents: Security-Updates zuerst, dazu ob ein Neustart
nötig ist. Operatoren der Gruppe können von Hand aktualisieren und neu starten.

## Woher die Liste kommt

Der Agent fragt alle 6 Stunden, nach jedem Lauf des Paketmanagers und auf Anforderung (**Neu auflisten**) den Root-Helper
nach den Updates (apt, dnf oder apk). Namen, Versionen und Quellen stammen aus den Paketquellen des Hosts und werden nur
als Text gezeigt. Alpine kennt keine Security-Updates: Dort gibt es nur „alle“.

## Jetzt aktualisieren

1. **Jetzt aktualisieren** zeigt die Pakete, die installiert würden – nur die Security-Updates oder alle.
2. Mit **Danach neu starten** startet der Node neu, wenn er es braucht und der Host es erlaubt.
3. Nach der Bestätigung (mit erneuter Anmeldung) läuft ein Rollout mit dem `maxParallel` der Gruppe. Reboot- und
   Offline-Alerts des Nodes sind währenddessen stumm.

## Was der Host erlaubt

In `/etc/sermon/helper.toml` entscheidet der Host selbst: `allow_remote_updates` (ab Werk an) und `allow_remote_reboot`
(ab Werk aus). Der Server kann das nicht ändern; die Knöpfe sind dann gesperrt und sagen warum. Ohne Root-Helper gibt
es keine Updates aus der Ferne.
