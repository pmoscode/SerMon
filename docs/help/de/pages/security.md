# Sicherheit

Hier richtest du die Zwei-Faktor-Anmeldung für dein Konto ein: Nach dem Passwort fragt SerMon zusätzlich einen Code aus einer Authenticator-App ab.

## Einrichten

1. „Einrichten“ wählen und – falls gefragt – das Passwort bestätigen.
2. Den QR-Code mit der App scannen (z. B. Aegis, 2FAS, Google Authenticator). Alternativ den Schlüssel von Hand eintragen.
3. Den angezeigten 6-stelligen Code eingeben.
4. Die **Recovery-Codes** sicher aufbewahren. Sie werden nur einmal angezeigt; jeder ersetzt die App ein einziges Mal.

## Passkeys

Ein Passkey meldet dich ohne Passwort an: Dein Gerät (Fingerabdruck, Gesicht, PIN) oder ein Sicherheitsschlüssel bestätigt dich, und er funktioniert nur auf dieser SerMon-Instanz – eine nachgemachte Seite kann ihn nicht abgreifen. Passkeys brauchen einen Domainnamen: Erreichst du SerMon über eine IP-Adresse oder einen Namen ohne Punkt, fehlt der Abschnitt.

- **Hinzufügen:** Namen eingeben (z. B. „Laptop“), „Passkey hinzufügen“ wählen und dem Browser folgen. SerMon fragt vorher noch einmal nach der Bestätigung. Bis zu 10 Passkeys pro Konto.
- **Anmelden:** auf der Anmeldeseite „Mit Passkey anmelden“, oder der Browser schlägt ihn im Feld für die E-Mail vor.
- Ein Passkey ist auch dein **zweiter Faktor**: nach dem Passwort statt des Codes und für Bestätigungen sensibler Aktionen. Verlangt deine Organisation zwei Faktoren, genügt ein Passkey.
- **Synchronisiert** heißt: Dein Passwort-Manager oder Gerätekonto sichert den Passkey; er steht dann auch auf deinen anderen Geräten bereit.
- Löschen verlangt eine Bestätigung. Löschst du den letzten zweiten Faktor, fragt eine Organisation mit Pflicht danach erst wieder nach der Einrichtung.
- Löschen meldet dich auf allen anderen Geräten ab: Ein verlorener Passkey nützt damit auch für schon offene Sitzungen nichts mehr. Ebenso beim Lösen einer SSO-Identität.

## Anmeldung über SSO

Hat die Instanz einen Identity Provider, verknüpfst du hier dein Konto damit („Mit … verknüpfen“, nach erneuter Bestätigung). Danach meldest du dich auf der Anmeldeseite über den Provider an. SerMon erkennt dich an deiner Identität beim Provider, nicht an der E-Mail.

- **Lösen** geht nur, wenn dir ein anderer Weg bleibt, meist dein Passwort. Konten, die SerMon bei der ersten Anmeldung über den Provider angelegt hat, haben kein Passwort.
- Bestätigungen für sensible Aktionen gehen auch über den Provider: Er fragt dann frisch nach der Anmeldung, danach führst du die Aktion noch einmal aus.
- Verlangt deine Organisation eine Zwei-Faktor-Anmeldung, zählt der zweite Faktor beim Provider, wenn der Betreiber das so eingestellt hat; sonst fragt SerMon nach der Anmeldung deinen eigenen Code ab.

## Gut zu wissen

- Jeder Code aus der App gilt nur einmal. Passt ein Code nicht, die Uhrzeit des Geräts prüfen.
- Bei Aktionen wie Rechteänderungen fragt SerMon den Code erneut ab; die Bestätigung gilt 15 Minuten.
- Verlangt deine Organisation die Zwei-Faktor-Anmeldung, sind ihre Seiten erst nach der Einrichtung erreichbar.
- Fünf falsche Codes sperren das Konto vorübergehend, genau wie falsche Passwörter.
