# 0034 – Passkeys mit WebAuthn

- Status: accepted
- Datum: 2026-11-24
- Bezug: KONZEPT.md §7 (D10); ADR 0007, ADR 0033; IMPLEMENTIERUNGSPLAN M3-23, M3-24; Threat Model T15, T87

## Kontext

Passwort und TOTP lassen sich abphishen; Passkeys (WebAuthn) binden die Anmeldung an die Origin und sind die
phishing-resistente Anmeldung aus D10 (T15). Sie sollen als zweiter Faktor, als Anmeldung ohne Passwort und als
Step-up dienen. WebAuthn hat viele Formate (CBOR, COSE-Schlüssel, Attestation-Varianten), die man nicht selbst parsen
will.

## Optionen

1. **Eigene Implementierung** nur für `none`-Attestation und ES256: wenig Code, aber CBOR, COSE und die Prüfungen der
   Spezifikation (Flags, RP-ID-Hash, Zähler, Origins) wären selbst geschrieben.
2. **`go-webauthn/webauthn`:** die verbreitete Go-Bibliothek (BSD-3), gepflegt, mit Fuzz-Tests. Sie bringt alle
   Attestation-Formate mit (u. a. `go-tpm`, `golang-jwt` für Metadaten), die SerMon nicht nutzt.

## Entscheidung

Gewählt: **Option 2**, mit `attestation: none` (keine Herstellerprüfung, keine Metadaten).

- **RP:** RP-ID ist der Host der ersten Origin aus `SERMON_ALLOWED_ORIGINS`; erlaubt sind die Origins mit diesem Host
  oder einer Subdomain davon. Ein Passkey gilt damit für genau eine Instanz.
- **Speicher:** `webauthn_credentials` pro User (instanzweit wie TOTP): Credential-ID, öffentlicher Schlüssel,
  Zähler, Flags (Backup), AAGUID, Name, letzte Nutzung; höchstens 10 pro User. User-Handle ist die User-ID (keine
  persönlichen Daten).
- **Zeremonien:** Die Sitzungsdaten von go-webauthn liegen in `webauthn_ceremonies`, 5 Minuten gültig und einmal
  nutzbar, gebunden an die Session (Registrierung, Step-up, zweiter Faktor nach dem Passwort) oder für die Anmeldung
  ohne Passwort an ein Cookie des Browsers, wie die OIDC-Flows (ADR 0033).
- **Registrierung** nur mit frischem Step-up; `residentKey: preferred`, `userVerification: preferred`.
- **Anmeldung ohne Passwort** nur mit einem auffindbaren Passkey und User Verification: Das zählt als zwei Faktoren
  (Org-Pflicht erfüllt). Als zweiter Faktor nach dem Passwort und als Step-up genügt die Anwesenheit.
- **Zähler:** Liefert der Authenticator einen Zähler, muss er steigen; sonst wird abgelehnt und ein Security-Ereignis
  der Instanz geschrieben (möglicher Klon). Synchronisierte Passkeys melden immer 0: Das ist erlaubt.
- **Fehlversuche** zählen zur Kontosperre wie falsche Passwörter, das Rate-Limit pro IP gilt (30 pro Minute). Konten
  ohne Passwort (über OIDC angelegt) haben keinen Sperrzähler; für sie gilt nur das Rate-Limit.
- **Löschen** mit Step-up und Audit (Audit-Log der Instanz: `passkey.added`, `passkey.deleted`). Wie beim Abschalten
  von TOTP darf auch der letzte zweite Faktor gehen; verlangt eine Org zwei Faktoren, erreicht der User sie danach nur
  noch für die Einrichtung.
- **API:** `PasskeyService` (Liste, Hinzufügen, Umbenennen, Löschen; Session genügt, auch während eine Org auf den
  zweiten Faktor wartet) und `AuthService.BeginPasskey`/`FinishPasskey` für Anmeldung, zweiten Faktor und Step-up. Die
  Optionen gehen als JSON für `PublicKeyCredential.parse…OptionsFromJSON` an den Browser. Die Session-Ansicht nennt
  `totp_enabled`, `passkey_count` und `passkeys_available`.

## Konsequenzen

- Mehr Abhängigkeiten (`go-webauthn` mit `fxamacker/cbor`, `go-tpm`, `golang-jwt`, `msgp`), von Renovate und
  govulncheck verfolgt; die nicht genutzten Formate werden trotzdem mitgebaut.
- Ändert der Betreiber die erste Origin auf einen anderen Host, gelten die vorhandenen Passkeys nicht mehr.
- Ohne Attestation weiß SerMon nicht, welcher Authenticator dahintersteht; die AAGUID dient nur der Anzeige.

## Nachtrag M3-28

- Ein rückwärts laufender Zähler wird abgewiesen und als `passkey.clone_suspected` (User und Credential-ID) ins
  Audit-Log der Instanz geschrieben; vorher stand er nur im Log des Servers.
- Eine Session aus einer Anmeldung mit Passkey (zwei Faktoren) verlangt in der Session-Ansicht keine Einrichtung
  eines zweiten Faktors.
- Mit einem zweiten Faktor bietet der Step-up-Dialog den Provider nur an, wenn dessen Anmeldung zwei Faktoren zählt
  (ADR 0033, Nachtrag M3-28).

