# Node hinzufügen

Bindet einen Linux-Server mit einem Befehl ein: Gruppe wählen, Befehl erzeugen und auf dem Server als root ausführen.

## Ablauf

1. Gruppe, Gültigkeit und Zahl der Nutzungen wählen. Der Befehl enthält ein **Enrollment-Token**, das nur jetzt angezeigt wird.
2. Den Befehl auf dem Server ausführen. Das Skript prüft die **Signatur** des Agents, legt den User `sermon` an, richtet den Dienst ein und meldet den Server an.
3. Diese Seite zeigt, sobald der Server verbunden ist und die ersten Metriken eintreffen.

## Gut zu wissen

- „Skript erst ansehen“ öffnet das Installationsskript als Text.
- Bei einem selbstsignierten Zertifikat `--ca-fingerprint sha256:…` anhängen (Fingerprint des Server-Zertifikats).
- Das Token wird nur als Hash gespeichert und gilt höchstens so oft und so lange wie gewählt.
- Erscheint der Server nicht: Erreicht er den Server auf Port 443 und den Agent-Port (Standard 8443)? Die Ausgabe des Skripts nennt den Grund.
