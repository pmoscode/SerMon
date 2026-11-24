# 0033 – Anmeldung über OpenID Connect

- Status: accepted
- Datum: 2026-11-24
- Bezug: KONZEPT.md §7 (D10); ADR 0007, ADR 0024; IMPLEMENTIERUNGSPLAN M3-20; Threat Model T15, T84, T85

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
  (`/login?sso=failed`); ob ein Konto existiert, verrät die Antwort nicht. Konten anlegen kommt mit M3-21.
- **Zweiter Faktor:** Die Pflicht einer Org gilt als erfüllt, wenn der Betreiber dem Provider vertraut
  (`SERMON_OIDC_MFA=trusted`) oder `amr` einen zweiten Faktor nennt (`mfa`, `mca`, `otp`, `hwk`, `sc`); die Session
  merkt sich das (`external_mfa`). Sonst gilt der lokale zweite Faktor: Wer einen hat, gibt ihn danach ein.
- **Step-up:** mit dem lokalen zweiten Faktor oder einer frischen Anmeldung beim Provider (`prompt=login`,
  `max_age=0`); `auth_time` muss nach dem Start des Flows liegen (1 min Toleranz), die Identität dem User gehören.
- **Weiterleitung:** nur auf Pfade der App (kein Schema, Host, `//`, Backslash, Punktsegment); die Antwort trägt
  `Referrer-Policy: no-referrer`, der Code steht in ihrer URL.
- **Grenzen:** 30 Starts und Rückkehrer pro Minute und IP.

## Konsequenzen

- Drei neue Abhängigkeiten (`go-oidc`, `x/oauth2`, `go-jose`), von Renovate und govulncheck verfolgt.
- Ein Provider, der `amr` nicht setzt (Dex, Keycloak ohne Mapper), erfüllt eine 2FA-Pflicht nur mit `trusted`. Das
  muss der Betreiber bewusst einstellen.
- Konten ohne Passwort (M3-21) bestätigen den Step-up beim Provider oder mit dem lokalen zweiten Faktor.
- Abmelden beim Provider (RP-initiated Logout) gibt es nicht: Abmelden beendet nur die Session in SerMon.
