# Vollstes Dateisystem

Der Füllstand des am stärksten belegten Dateisystems des Servers – berechnet wie bei `df`: belegt geteilt durch belegt plus für normale Benutzer verfügbar.

## Einordnung

- Ab etwa 90 % wird es eng: Logs, Datenbanken und Updates können fehlschlagen, wenn der Platz ausgeht.
- Ein volles Dateisystem kann auch bei freiem Platz auftreten, wenn keine **Inodes** mehr frei sind (viele kleine Dateien).
- Das Node-Detail zeigt jedes Dateisystem einzeln und schätzt, wann es bei gleichem Trend voll ist.

## Nächste Schritte

1. Im Node-Detail das betroffene Dateisystem suchen.
2. Große Verzeichnisse finden, z. B. mit `du -xh --max-depth=1 / | sort -h`.
3. Alte Logs, Pakete im Cache oder Sicherungen aufräumen.
