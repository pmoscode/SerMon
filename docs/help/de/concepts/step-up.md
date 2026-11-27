# Erneute Bestätigung

Vor sensiblen Aktionen fragt SerMon noch einmal nach, dass wirklich du am Gerät sitzt: mit dem Code der
Authenticator-App, einem Passkey, über den Identity Provider oder – ohne zweiten Faktor – mit dem Passwort. Die
Bestätigung gilt danach 15 Minuten für alle Aktionen.

## Wann SerMon fragt

- **Rechte:** Rollen vergeben und entziehen, Mitglieder entfernen, Einladen, Gruppen löschen, SSO-Gruppen zuordnen.
- **Eigenes Konto:** Anzeigenamen und E-Mail-Adresse ändern, die eigenen Daten exportieren.
- **Anmeldung:** zweiten Faktor einrichten oder abschalten, Recovery-Codes neu erzeugen, Passkeys hinzufügen und
  löschen, eine SSO-Identität lösen, die Pflicht zur Zwei-Faktor-Anmeldung ändern.
- **Was alarmiert:** Alert-Regeln importieren, ein- und ausschalten oder zurücksetzen, Stummschaltungen anlegen,
  Kanäle, Richtlinien und Wartungsfenster ändern, Alerts aus Sicherheitsregeln quittieren.
- **Nodes:** Enrollment-Token erzeugen, Schlüssel erneuern, freigeben, Übernahme erlauben, Tags setzen,
  deaktivieren, löschen und das Löschen abbrechen.
- **Eingriffe auf den Hosts:** Updates, Reboots und Agent-Updates starten oder abbrechen, Update-Richtlinien ändern,
  Updates pausieren, Module ein- und ausschalten.
- **Daten:** Log-Einstellungen und Redaction ändern, wer Logs und Befehlszeilen sieht, die Aufbewahrung ändern,
  Kiosk-Tokens erzeugen.

Was nur Zugriff wegnimmt oder nur die eigene Ansicht betrifft – eine Stummschaltung beenden, ein Token widerrufen, ein
Dashboard ändern – braucht keine Bestätigung.

## Gut zu wissen

- Läuft die Bestätigung über den Identity Provider, meldest du dich dort frisch an und führst die Aktion danach noch
  einmal aus.
- Ein Wechsel der Organisation behält die Bestätigung, gibt aber keine Rechte.
- Fünf falsche Codes oder Passwörter sperren das Konto vorübergehend.
- Mehr zu den Faktoren: [Sicherheit](help:pages/security).
