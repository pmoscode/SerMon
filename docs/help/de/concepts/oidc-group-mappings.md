# SSO-Gruppen

Gruppen des Identity Providers geben Rollen in deiner Organisation: Wer beim Provider in der Gruppe ist, bekommt die Rolle bei der nächsten Anmeldung über SSO – und verliert sie, sobald er nicht mehr in der Gruppe ist.

## So wirken Zuordnungen

- Eine Zuordnung nennt die Gruppe genau wie im Claim `groups` des Providers, eine Rolle und eine Node-Gruppe oder alle Gruppen. Passen mehrere, gilt pro Node-Gruppe die höchste Rolle.
- Abgeglichen wird bei jeder Anmeldung über den Provider. Wer angemeldet bleibt, behält die Rollen bis zur nächsten Anmeldung; wer sofort entziehen muss, tut das in der Mitgliederliste.
- Rollen, die ein Admin von Hand oder per Einladung vergeben hat, bleiben unberührt. Vergibst du eine Rolle aus einer Zuordnung von Hand neu, gehört sie dir: Die Anmeldung ändert sie nicht mehr.
- Der letzte Admin für alle Gruppen bleibt, auch wenn der Provider ihm die Gruppe nimmt.

## Neue Mitglieder

Ist das Anlegen von Konten eingeschaltet, bekommt eine Person, die SerMon noch nicht kennt, bei der Anmeldung ein Konto und wird Mitglied, wenn eine Zuordnung passt. Das gilt nur mit bestätigter E-Mail und nur, wenn noch kein Konto diese E-Mail hat; im Kontingent der Organisation muss Platz sein. Wer alle Rollen verliert, bleibt Mitglied ohne Rechte.
