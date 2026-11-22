# 0024 – Ausgehende Verbindungen: `safehttp`, Allowlist in der Server-Konfiguration

- Status: accepted
- Datum: 2026-11-21
- Bezug: KONZEPT.md §18.6, §12.4; ADR 0019; Threat Model T43; IMPLEMENTIERUNGSPLAN M2-09

## Kontext

Ab M2-10 schickt der Server Requests an Ziele, die Org-Admins eintragen: Webhooks, Slack, Teams, Discord, Mattermost,
ntfy, Gotify und so weiter. Ohne Schutz wird daraus SSRF: Eine Webhook-URL auf `http://169.254.169.254/` liest die
Zugangsdaten der Cloud-Instanz, eine auf `http://10.0.0.5:5432/` tastet das interne Netz ab. Die Antwort oder
zumindest der Fehlertext kommt über „Test senden“ und das Zustellprotokoll zurück.

Angriffswege:

- **Adressen:** Literale wie `127.0.0.1`, `[::1]` und `[::ffff:169.254.169.254]`; Namen, die auf interne Adressen
  zeigen; IPv6-Formen mit eingebetteter IPv4 (NAT64, 6to4).
- **DNS-Rebinding:** Der Name zeigt bei der Prüfung auf eine öffentliche Adresse und beim Verbinden auf eine interne.
- **Redirects:** Ein öffentliches Ziel leitet auf ein internes um.
- **Proxy:** `HTTPS_PROXY` aus der Umgebung lässt den Proxy das Ziel auflösen; die Prüfung wäre wirkungslos.
- **Lecks:** Webhook-URLs enthalten Geheimnisse in Pfad und Query (Slack, Discord). Go nennt die URL in jedem Fehler.

Interne Ziele sind trotzdem legitim, etwa ein internes Mattermost oder ntfy. KONZEPT §18.6 sieht dafür eine Allowlist
vor, die „der Superadmin konfiguriert“.

## Optionen

1. **Allowlist in der UI durch den Superadmin** (Tabelle, RPC, Audit).
2. **Allowlist in der Server-Konfiguration** (`SERMON_OUTBOUND_ALLOWLIST`, CIDRs) durch den Betreiber.
3. **Keine Allowlist,** interne Ziele nur über einen eigenen Relay-Dienst.

## Entscheidung

Option 2 und ein eigener Client `internal/platform/safehttp` für alle Requests an konfigurierte Ziele:

- **Gesperrt** sind die Bereiche der IANA-Register für Sonderadressen: unspezifiziert, „this network“, Loopback,
  privat, CGNAT, Link-local, IETF-Protokolle (`192.0.0.0/24`), Benchmarking, Multicast, reserviert, ULA, Site-local,
  Discard, Teredo, 6to4 und lokales NAT64. IPv4-mapped und Well-known-NAT64 werden auf ihre IPv4 geprüft.
  Die Dokumentationsbereiche sind frei, weil sie nirgends geroutet werden; die Tests nutzen sie als öffentliche Ziele.
- **Pinning:** Der Client löst den Namen selbst auf, prüft jede Antwort (eine schlechte Adresse lehnt den Namen ab) und
  verbindet sich nur mit geprüften Adressen. Zusätzlich prüft `Dialer.Control` die Adresse des Sockets.
- **Redirects:** höchstens 3, jeder wird erneut geprüft, kein Wechsel von https auf http.
- **http** nur zu Zielen auf der Allowlist; öffentliche Ziele brauchen TLS (mindestens 1.2).
- **Kein Proxy** aus der Umgebung.
- **Grenzen:** 15 s pro Request mit Redirects, Antwort höchstens 1 MiB, Header höchstens 64 KiB.
- **Fehler** nennen Methode und Host, nie Pfad oder Query; auch der Parser-Fehler zitiert die URL nicht.
- **Allowlist:** Netze oder Adressen, getrennt durch Kommas. Abgelehnt werden `/0`, IPv4-mapped-Präfixe und alles, was
  einen Metadata-Dienst enthält (`169.254.169.254`, `fd00:ec2::254`, `100.100.100.200`, `192.0.0.192`).
- **Durchsetzung:** Die Lint-Regel `forbidigo` verbietet `http.Client`, `http.Transport`, `http.Get` und Verwandte
  außerhalb von `safehttp` und Tests.

## Konsequenzen

- Positiv: Ein übernommenes Konto (auch das des Superadmins) kann das interne Netz nicht öffnen; das kann nur, wer den
  Server betreibt. Keine Tabelle, kein RPC und keine Rollen-Matrix für die Allowlist.
- Negativ: Eine Änderung der Allowlist braucht einen Neustart. Ein Egress-Proxy wird nicht unterstützt; wer ihn
  braucht, bekommt ihn später als eigene Option mit Prüfung vor dem Proxy.
- Abweichung von KONZEPT §18.6: Die Allowlist setzt der Betreiber statt des Superadmins in der UI.
- SMTP ist nicht betroffen: Den Mailserver legt der Betreiber fest (`SERMON_SMTP_HOST`), nicht die Org.
- Folgearbeiten: Notifier nutzen `safehttp` (M2-10); `Policy.ValidateURL` prüft Ziele schon beim Speichern eines
  Kanals; Selbst-Metriken für gesperrte Requests mit dem Zustellprotokoll.

## Nachtrag M2-28 (Security-Review)

- **Eingebettete IPv4 in der Allowlist:** NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`), IPv4-kompatibel (`::/96`), 6to4 und
  Teredo tragen eine IPv4-Adresse. Allowlist-Einträge, die diese Bereiche überlappen, lehnt `NewPolicy` ab (außer
  `::1/128`); eine NAT64-Adresse wird vor der Allowlist ausgepackt. Vorher öffnete `64:ff9b::/96` den Metadata-Dienst.
- **Redirects nur auf denselben Host:** Go entfernt bei einem Wechsel des Hosts nur `Authorization` und Cookies, eigene
  Header wie `X-Gotify-Key` gingen mit.
