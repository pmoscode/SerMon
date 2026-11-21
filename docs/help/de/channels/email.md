# E-Mail

Benachrichtigungen als E-Mail mit Text- und HTML-Teil über den Mailserver der SerMon-Installation.

## Einrichten

- **Empfänger:** bis zu 10 Adressen, durch Kommas getrennt, z. B. `ops@example.org, bereitschaft@example.org`. Jeder Empfänger bekommt eine eigene Mail und sieht die anderen nicht.

Den Mailserver legt der Betreiber fest (`SERMON_SMTP_*`, immer mit TLS). Ohne ihn gibt es diesen Kanal nicht.

## Zustellung

Lehnt der Mailserver eine Adresse dauerhaft ab (Fehler 5xx), bricht SerMon ab; vorübergehende Fehler (4xx, Netzwerk) versucht es bis zu sechsmal erneut. Prüfe bei Problemen den Spam-Ordner und ob die Absenderadresse der Installation per SPF und DKIM erlaubt ist.
