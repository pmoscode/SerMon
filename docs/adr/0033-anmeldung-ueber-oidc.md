# 0033 – Anmeldung über OpenID Connect

- Status: accepted
- Datum: 2026-11-24
- Bezug: KONZEPT.md §7 (D10); ADR 0007, ADR 0024; IMPLEMENTIERUNGSPLAN M3-20, M3-21, M3-22; Threat Model T15, T84, T85, T86

## Kontext

Viele Betreiber haben einen Identity Provider (Keycloak, Authentik, Entra ID, Dex) und wollen keine zweiten Passwörter
pflegen (D10). Der Server muss eine fremde Anmeldung annehmen, ohne dass eine gestohlene Antwort, ein umgeleiteter
Browser oder eine übereinstimmende E-Mail-Adresse ein Konto öffnet. Er läuft mit mehreren Instanzen; ein Flow, der auf
einer beginnt, kann auf einer anderen enden.

## Optionen

1. **Eigene Implementierung** von Authorization Code, Discovery und JWT-Prüfung: keine Abhängigkeit, aber die
   heikelsten Teile (Signaturprüfung, Algorithmen, Schlüsselwechsel) wären selbst geschrieben.
2. **`coreos/go-oidc` mit `x/oauth2`:** verbreitete, gepflegte Bibliotheken (Apache 2.0 bzw. BSD) mit Discovery, JWKS
   mit Schlüsselwechsel und ID-Token-Prüfung. Über `go-jose` kommt eine weitere Abhängigkeit dazu.
3. **Ein Proxy davor** (oauth2-proxy): keine Änderung im Server, aber keine Rechte aus Gruppen (M3-21), kein Step-up
   und ein weiterer Dienst.

## Entscheidung

Gewählt: **Option 2**, ein Provider pro Instanz (`SERMON_OIDC_*`, das Secret als Datei).

- **Ablauf:** Authorization Code mit PKCE (S256). `BeginOidc` (Connect, mit Origin-Prüfung) erzeugt `state`, `nonce` und
  den Verifier, speichert den Flow 10 Minuten in `oidc_flows` (für jede Instanz sichtbar) und bindet ihn an ein
  Cookie `__Host-sermon_oidc` (SameSite=Lax). Der Provider schickt den Browser an `/auth/oidc/callback` (GET, ohne
  Origin); der Flow wird dort einmal genommen, das Cookie muss passen. Ein fremder Browser mit Code und State bekommt
  keine Session (Login-CSRF).
- **Prüfung:** go-oidc prüft Signatur (nur asymmetrische Verfahren, nie `none`), Issuer, Audience und Ablauf, der
  Server die `nonce`. Discovery, Schlüssel und Token-Anfrage laufen über `safehttp` (ADR 0024): Ein Provider im
  internen Netz braucht die Allowlist des Betreibers. Die Discovery folgt beim ersten Gebrauch, ein Ausfall beim Start
  hält den Server nicht an.
- **Identität:** immer `iss` + `sub` (`user_identities`), nie die E-Mail. Verknüpft wird nur angemeldet mit frischem
  Step-up, ein Eintrag im Audit-Log der Instanz. Eine unbekannte Identität wird abgewiesen wie jeder Fehler
  (`/login?sso=failed`); ob ein Konto existiert, verrät die Antwort nicht. Konten anlegen: siehe unten (M3-21).
- **Zweiter Faktor:** Die Pflicht einer Org gilt als erfüllt, wenn der Betreiber dem Provider vertraut
  (`SERMON_OIDC_MFA=trusted`) oder `amr` einen zweiten Faktor nennt (`mfa`, `mca`, `otp`, `hwk`, `sc`); die Session
  merkt sich das (`external_mfa`). Sonst gilt der lokale zweite Faktor: Wer einen hat, gibt ihn danach ein.
- **Step-up:** mit dem lokalen zweiten Faktor oder einer frischen Anmeldung beim Provider (`prompt=login`,
  `max_age=0`); `auth_time` muss nach dem Start des Flows liegen (1 min Toleranz), die Identität dem User gehören.
- **Weiterleitung:** nur auf Pfade der App (kein Schema, Host, `//`, Backslash, Punktsegment); die Antwort trägt
  `Referrer-Policy: no-referrer`, der Code steht in ihrer URL.
- **Grenzen:** 30 Starts und Rückkehrer pro Minute und IP.

### Gruppen und Konten (M3-21)

- **Zuordnung pro Org:** Admins für alle Gruppen ordnen eine Provider-Gruppe (exakter Name im Claim `groups`) einer
  Node-Gruppe oder `*` und einer Rolle zu (`oidc_group_mappings`, höchstens 200, Step-up und Audit).
- **Abgleich bei jeder Anmeldung**, bevor die Session entsteht; schlägt er fehl, scheitert die Anmeldung (keine Session
  mit entzogenen Rollen). Die Bindung trägt ihre Herkunft (`role_bindings.origin`): Zuordnungen setzen, ändern und
  entziehen nur Bindungen mit `oidc`. Vergibt ein Admin eine Rolle von Hand (oder per Einladung), wird sie `manual`
  und bleibt. Gibt es für eine Node-Gruppe eine Bindung von Hand, bleibt sie, auch wenn eine Zuordnung mehr gäbe.
- Fehlt der Claim, gilt das als „keine Gruppen“: Die zugeordneten Rollen gehen (sicherer Ausfall).
- **Grenzen:** Der letzte org-weite Admin bleibt (Warnung im Log). Gesperrte Orgs werden nicht abgeglichen und nimmt
  niemand neu auf. Das Mitglieder-Kontingent gilt für neue Mitgliedschaften. Wer alle Rollen verliert, bleibt Mitglied
  ohne Rechte; das Entfernen bleibt dem Admin.
- **JIT** (Standard an, `SERMON_OIDC_JIT=false`): Eine unbekannte Identität bekommt ein Konto ohne Passwort, wenn die
  E-Mail bestätigt ist (`email_verified`), keinem Konto gehört und eine Zuordnung einer ungesperrten Org passt; dann
  wird sie Mitglied der passenden Orgs – nur bei dieser ersten Anmeldung (Nachtrag M3-28). Gehört die E-Mail einem Konto, entsteht keines: Die Identität muss dort
  verknüpft werden (keine Übernahme über die E-Mail). Welche Orgs passen, ermittelt eine SECURITY-DEFINER-Funktion,
  die nur IDs liefert.
- **Audit:** Zuordnungen im Audit-Log der Org (`oidc_mapping.create`/`delete`), Änderungen bei der Anmeldung als
  `role.grant`, `role.revoke`, `member.join` ohne Akteur mit `origin: oidc`, neue Konten im Audit-Log der Instanz
  (`oidc.user_provisioned`).

### Oberfläche (M3-22)

- Die Anmeldeseite zeigt den Knopf des Providers nur, wenn einer eingerichtet ist (`GetLoginOptions`). Fehler landen
  ohne Details auf `/login?sso=failed`; mit lokalem zweiten Faktor folgt die Code-Eingabe (`?sso=second_factor`).
- Die Session-Ansicht nennt `has_password` und `oidc_linked`. Der Step-up-Dialog bietet die Bestätigung beim Provider
  an, wenn eine Identität verknüpft ist; Konten ohne Passwort und zweiten Faktor bestätigen nur dort. Nach der
  Rückkehr wird die Aktion von Hand wiederholt (die Seite wird verlassen).
- **Lösen** (`UnlinkOidcIdentity`) mit frischem Step-up, nur wenn ein anderer Weg bleibt (Passwort oder eine weitere
  Identität), mit Eintrag im Audit-Log der Instanz.
- E2E gegen Dex (Mock-Connector mit Gruppe) in der Produktions-Compose mit Overlay.

## Konsequenzen

- Drei neue Abhängigkeiten (`go-oidc`, `x/oauth2`, `go-jose`), von Renovate und govulncheck verfolgt.
- Ein Provider, der `amr` nicht setzt (Dex, Keycloak ohne Mapper), erfüllt eine 2FA-Pflicht nur mit `trusted`. Das
  muss der Betreiber bewusst einstellen.
- Konten ohne Passwort (M3-21) bestätigen den Step-up beim Provider oder mit einem Passkey.
- Abmelden beim Provider (RP-initiated Logout) gibt es nicht: Abmelden beendet nur die Session in SerMon.
- Rollen aus Gruppen ändern sich erst bei der nächsten Anmeldung; eine laufende Session behält sie bis dahin (höchstens
  12 h). Wer sofort entziehen muss, entzieht von Hand oder entfernt das Mitglied.
- Mit JIT kann der Admin einer Org Mitglieder einer Provider-Gruppe aufnehmen und sieht ihre E-Mail. Wer das nicht
  will, schaltet JIT ab.

## Nachtrag M3-28: Befunde des Security-Reviews

- **Step-up beim Provider** gilt für User mit eigenem zweiten Faktor (TOTP oder Passkey) nur, wenn die Anmeldung dort
  ebenfalls zwei Faktoren hatte (`amr` oder `SERMON_OIDC_MFA=trusted`); sonst wird er abgewiesen. Vorher ersetzte das
  Passwort beim Provider den lokalen Faktor, und mit dem Step-up ließ sich ein eigener Passkey hinzufügen.
  `GetLoginOptions.oidc_second_factor` sagt der UI, ob sie den Weg diesen Usern anbieten darf.
- **Beitritt nur bei der Anlage:** Bestehende Konten bekommen und verlieren bei der Anmeldung nur die Rollen in Orgs,
  in denen sie Mitglied sind. Vorher trat ein von einem Admin entferntes Mitglied bei der nächsten Anmeldung über die
  Zuordnung wieder bei. Wer ein vorhandenes Konto aufnehmen will, lädt es ein.
- **Kein TOTP ohne Passwort:** Die Kontosperre für Codes hängt am Passwort; Konten aus JIT nutzen Passkeys als zweiten
  Faktor (`BeginTotpSetup` antwortet `FAILED_PRECONDITION`).
- **Anzeigenamen** aus dem Claim `name` gelten nur nach den Regeln aller Namen (`naming.Clean`), sonst der lokale Teil
  der E-Mail.
- Die UI verlässt die App nur zu einer https-Adresse des Providers.

## Nachtrag M3-29

- Start und Rückkehr zählen in getrennten Rate-Limits (30 pro Minute und IPv4-Adresse bzw. IPv6-/64): Viele Starts
  hinter einer Adresse blockieren die Rückkehr der anderen nicht. Offene Flows höchstens 10 000 (in der Abfrage des
  Einfügens geprüft); ein abgewiesener Start erreicht den Provider nicht.
- Das Lösen einer Identität beendet die übrigen Sessions des Users: Eine verlorene Identität nützt auch für schon offene
  Sessions nichts mehr.
