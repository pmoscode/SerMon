# Konto

Dein eigenes Konto: Anzeigename, E-Mail-Adresse für die Anmeldung und der Export aller Daten, die SerMon über dich
hält. Jede Änderung und der Export fragen vorher noch einmal nach deiner Bestätigung.

## Name und E-Mail-Adresse

- Den **Anzeigenamen** sehen andere Mitglieder, z. B. bei Quittierungen und im Audit-Log.
- Eine neue **E-Mail-Adresse** gilt erst mit dem Code, den SerMon an sie schickt: Er gilt eine Stunde und fünf
  Versuche lang. Danach meldest du dich mit der neuen Adresse an; die alte bekommt eine Nachricht über die Änderung.
  Gehört die Adresse schon zu einem anderen Konto, lehnt SerMon sie nach dem Code ab.
- Ohne Mailserver (nur in der Entwicklung) gilt die neue Adresse sofort.
- Die Anmeldung über SSO hängt nicht an der Adresse, sondern an deiner Identität beim Provider.

## Meine Daten

**Daten exportieren** lädt eine JSON-Datei herunter: Konto, Passwort (nur seit wann), zweiter Faktor und Passkeys
ohne Geheimnisse, SSO-Verknüpfungen, Sitzungen, Mitgliedschaften mit Rollen, deine Dashboards und deine Einträge im
Audit-Log (Zeit und Aktion). Daten anderer Personen enthält sie nicht. Der Export steht im Audit-Log der Instanz.

## Wann ein Konto gelöscht wird

- Endet deine letzte Mitgliedschaft, löscht SerMon das Konto nach einer Frist (Standard 30 Tage); wirst du vorher
  wieder eingeladen, bleibt es mit Passkeys und zweitem Faktor erhalten.
- Auf Antrag löscht der Betreiber der Instanz das Konto sofort ([Organisationen](help:pages/organizations)).
- Im Audit-Log bleibt danach nur eine Kennung, kein Name und keine Adresse. Mehr zur Bestätigung:
  [Erneute Bestätigung](help:concepts/step-up).
