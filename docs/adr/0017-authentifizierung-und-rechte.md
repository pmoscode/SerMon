# 0017 – Authentifizierung und Rechte: lokale Konten mit 2FA, OIDC, Rollen pro Node-Gruppe

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D10, D11), §7; ADR 0007, 0008; IMPLEMENTIERUNGSPLAN M1-01 bis M1-04

## Kontext

Wer SerMon bedient, kann Updates und Reboots auf vielen Servern auslösen und sieht Logs und Security-Events. Die
Anmeldung muss deshalb stark sein, darf aber kein externes System voraussetzen (kleine Installationen ohne IdP).
Rechte müssen feiner als „Admin oder nicht“ sein: Ein Team betreut nur seine Server.

## Optionen

1. **Nur OIDC:** keine Passwörter in SerMon, aber ohne IdP nicht nutzbar.
2. **Nur lokale Konten:** einfach, aber kein SSO für größere Umgebungen.
3. **Lokale Konten mit 2FA plus optional OIDC.**
4. Rechte **global pro User**, **pro Organisation** oder **pro Node-Gruppe** mit festen Rollen; frei definierbare
   Rollen als Alternative.

## Entscheidung

- **D10 – Lokal (Argon2id) + TOTP + Passkeys/WebAuthn, OIDC/SSO:** Passwörter mit Argon2id, Mindestlänge und
  Sperrliste; TOTP mit Recovery-Codes (M1-03), Passkeys und OIDC (Authorization Code + PKCE, Gruppen-Mapping) folgen
  in M3. Sessions serverseitig mit `__Host-`-Cookie, Idle- und Absolut-Timeout, Step-up für sensible Aktionen.
- **D11 – Org → Node-Gruppen → Rollen:** Eine Rolle (Viewer, Operator, Admin) gilt für eine Gruppe oder alle Gruppen
  einer Organisation; mehrere Rollen ergänzen sich. Die Policy verweigert alles, was nicht ausdrücklich erlaubt ist;
  der Superadmin verwaltet Organisationen, sieht aber keine Mandantendaten.

## Konsequenzen

- Positiv: SerMon funktioniert ohne IdP und trotzdem mit zweitem Faktor; Teams lassen sich auf ihre Gruppen begrenzen;
  die feste Rollen-Matrix ist vollständig testbar (ein Test erzwingt, dass jede Aktion darin steht).
- Negativ: Feste Rollen decken Sonderfälle nicht ab; eigene Rollen bleiben ein späterer Ausbau.
- Security: Kontosperre gilt für Passwort und Codes gemeinsam; Rechte werden bei jedem Aufruf neu geladen, entzogene
  Rechte wirken sofort; Rechteänderungen verlangen Step-up und stehen im Audit-Log.
- Folgearbeiten: WebAuthn und OIDC in M3; org-gebundene API-Tokens nach v1.0 (O9).
