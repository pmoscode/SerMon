# 0009 – Enrollment über den Web-Endpunkt, danach mTLS

- Status: accepted
- Datum: 2026-11-19
- Bezug: KONZEPT.md §6 (Ablauf, Schritt 4), §18.2; IMPLEMENTIERUNGSPLAN M1-09, M1-10, M1-13; ADR 0007

## Kontext

Beim Enrollment hat der Agent noch kein Client-Zertifikat und kennt die interne Agent-CA nicht. KONZEPT §6 sieht vor,
dass er das Server-Zertifikat gegen die **System-CA** oder einen **gepinnten Fingerprint** prüft. Der mTLS-Port der
Agent-API (M1-10) nutzt dagegen ein Server-Zertifikat der internen CA und verlangt ein Client-Zertifikat.

## Optionen

1. **Enrollment auf dem mTLS-Port mit optionalem Client-Zertifikat.** Der Agent müsste der internen CA schon vorher
   vertrauen, also immer einen Fingerprint mitbekommen. Außerdem würde der Port Verbindungen ohne Client-Zertifikat
   annehmen; das schwächt die Regel „Client-Zertifikat Pflicht“.
2. **Enrollment über den öffentlichen HTTPS-Endpunkt der Web-UI** (gewählt). Caddy terminiert TLS mit einem
   ACME-Zertifikat, dem die System-CA vertraut. Bei selbstsignierten Zertifikaten pinnt der Agent den Fingerprint.
3. **Eigener dritter Port nur für Enrollment.** Mehr Betrieb (Firewall, Caddy), kein Sicherheitsgewinn gegenüber 2.

## Entscheidung

- `sermon.agent.v1.EnrollmentService.Enroll` läuft auf dem Web-Listener unter `/sermon.agent.v1.EnrollmentService/`.
- **Keine Origin-Prüfung, keine Session:** Agents senden weder Origin noch Cookies. Das Enrollment-Token ist das
  einzige Credential; CSRF ist ohne Cookies nicht möglich.
- **Rate-Limit pro Client-IP**, protovalidate für alle Felder, die CSR ist auf 4 KiB begrenzt.
- **Protokoll:** Der Agent spricht gRPC (tonic). Der Web-Listener nimmt deshalb neben HTTP/1.1 auch HTTP/2 ohne TLS
  (h2c) an; Caddy leitet mit h2c weiter. Von außen ist es immer TLS.
- **Antwort:** Agent-Zertifikat, Intermediate und Root der Agent-CA (Trust-Anker für den mTLS-Port), Node-ID.
- **Atomarität:** Der Server signiert die CSR *vor* der Transaktion. Danach verbraucht er das Token, legt den Node an,
  zeichnet das Zertifikat auf und schreibt das Audit, alles in einer Transaktion der Org. Scheitert sie, wird das
  Zertifikat nie ausgeliefert. Ein nicht aufgezeichnetes Zertifikat ist ohnehin wertlos, weil die Agent-API nur
  aufgezeichnete Serials akzeptiert (M1-07).
- **`machine-id`-Konflikt:** Gibt es in der Org schon einen Node mit derselben `machine-id`, antwortet der Server mit
  `ALREADY_EXISTS`, ohne das Token zu verbrauchen. Eine Übernahme gibt es nur nach Bestätigung in der UI (M1-32),
  nie automatisch.

## Konsequenzen

- Kein zusätzlicher Port; die Installation braucht nur die öffentliche URL des Servers.
- Der mTLS-Port bleibt streng: TLS 1.3, Client-Zertifikat Pflicht.
- Caddy muss für diesen Pfad h2c zum Server sprechen (Beispielkonfiguration mit M1-27).
- Wer den Web-Endpunkt erreicht, kann `Enroll` aufrufen. Ohne gültiges Token passiert nichts; die Antwort
  unterscheidet ungültig, abgelaufen und aufgebraucht nicht.
