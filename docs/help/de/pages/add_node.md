# Node hinzufügen

Bindet einen Linux-Server mit einem Befehl ein: Gruppe wählen, Befehl erzeugen und auf dem Server als root ausführen.

## Ablauf

1. Gruppe, Gültigkeit und Zahl der Nutzungen wählen. Der Befehl enthält ein **Enrollment-Token**, das nur jetzt angezeigt wird.
2. Den Befehl auf dem Server ausführen. Das Skript prüft die **Signatur** des Agents, legt den User `sermon` an, richtet den Dienst ein und meldet den Server an.
3. Diese Seite zeigt, sobald der Server verbunden ist und die ersten Metriken eintreffen.

## Gut zu wissen

- „Skript erst ansehen“ öffnet das Installationsskript als Text.
- Bei einem selbstsignierten Zertifikat `curl -k` verwenden und `--ca-fingerprint sha256:…` anhängen: den Fingerprint des Server-Zertifikats oder einer CA, die der Server mitschickt (z. B. die Intermediate-CA von Caddy `tls internal`). Die Signatur des Agents prüft das Skript in jedem Fall.
- Nodes gehören immer zu einer Gruppe. Gibt es noch keine, legen Admins sie unter „Organisation“ → „Gruppen“ an.
- Das Token wird nur als Hash gespeichert und gilt höchstens so oft und so lange wie gewählt.
- Erscheint der Server nicht: Erreicht er den Server auf Port 443 und den Agent-Port (Standard 8443)? Die Ausgabe des Skripts nennt den Grund.
