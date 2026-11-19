# CPU-Auslastung

Der Anteil der Rechenzeit, in der die Prozessoren nicht untätig waren – gemittelt über alle Kerne.

## Einordnung

- Dauerhaft über 80 % heißt: Der Server hat kaum Reserven, neue Last führt zu Wartezeiten.
- Kurze Spitzen bis 100 % sind normal, etwa bei Backups, Updates oder Builds.
- Einen hohen Wert verursacht nicht immer Rechenarbeit: Auch **iowait** (Warten auf Datenträger) und **steal** (Zeit, die der Hypervisor einer anderen VM gibt) zählen hier mit.

## Nächste Schritte

1. Im Node-Detail die Aufteilung nach `user`, `system`, `iowait` und `steal` ansehen.
2. Die Prozesse mit der höchsten CPU-Last suchen, z. B. mit `top`.
3. Bei hohem `steal` den Hoster bzw. die Auslastung des Hypervisors prüfen.
