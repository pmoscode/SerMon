# 0003 – UI-Auslieferung: eingebettete SPA, strikte CSP, keine externen Ressourcen

- Status: accepted
- Datum: 2026-11-18
- Bezug: KONZEPT.md D3, D27, §4.4, §17, §18.3, §21; IMPLEMENTIERUNGSPLAN M0-04

## Kontext

Die Web-UI (SvelteKit) wird vom Go-Server ausgeliefert.
Vorgabe des Product Owners: **Alle Bibliotheken, Schriften und sonstigen Ressourcen der UI kommen vom eigenen Server, nicht von einem CDN** (D27).

Gründe dafür:

- Datenschutz: keine IP-Adressen an Dritte, DSGVO.
- Betrieb in abgeschotteten Netzen ohne Internetzugang (Air-Gap).
- Eine strikte Content-Security-Policy.

Beim Aufsetzen sind drei Stellen aufgefallen, an denen Werkzeuge stillschweigend dagegen verstoßen:

1. **Paraglide/inlang** lädt seine Compiler-Plugins standardmäßig zur Build-Zeit von `cdn.jsdelivr.net`.
2. **Vite** bettet kleine Assets (Font-Subsets unter 4 KB) als `data:`-URI ins CSS ein; `font-src 'self'` blockiert das.
3. **SvelteKit** rendert für Screenreader einen Route-Announcer mit festem `style`-Attribut; `style-src 'self'` blockiert das.

## Optionen

### Für die CSP

- **A) `'unsafe-inline'` für `script-src`/`style-src`.**
  Einfach, aber es hebelt den XSS-Schutz der CSP weitgehend aus. → abgelehnt.
- **B) CSP als `<meta>`-Tag von SvelteKit (`kit.csp`).**
  Funktioniert im SPA-Modus nur eingeschränkt; `frame-ancestors` geht per Meta-Tag gar nicht. → abgelehnt.
- **C) Der Server berechnet die CSP beim Start aus dem eingebetteten Build** (gewählt):
  - Hashes aller Inline-Skripte in `index.html` (SvelteKit-Bootstrap) → `script-src 'self' 'sha256-…'`.
  - Hashes aller **festen** `style="…"`-Attribute in den eigenen JS-Bundles → `style-src 'self' 'unsafe-hashes' 'sha256-…'`.

    `'unsafe-hashes'` erlaubt **nur exakt diese Attributwerte**. Eingeschleuste Styles und `<style>`-Elemente bleiben blockiert.

### Für Assets

- `build.assetsInlineLimit = 0`: Alle Assets sind eigene Dateien, und `data:` ist für Schriften nicht nötig.

## Entscheidung

- **Auslieferung:**
  - SvelteKit 3 als SPA (`adapter-static`, Fallback `index.html`, kein SSR).
  - `just build-web` kopiert den Build nach `server/web/dist`, von dort wird er per `embed.FS` in die Server-Binary eingebettet.
  - Der Handler `internal/transport/spa` liefert existierende Dateien aus und beantwortet Client-Routen (Pfade ohne Dateiendung) mit `index.html`.
  - Unbekannte Dateien ergeben 404. `_app/immutable/*` wird ein Jahr gecacht (`immutable`), alles andere mit `no-cache`.
- **Security-Header:** strikte CSP (Option C) sowie `X-Content-Type-Options`, `X-Frame-Options: DENY`, `Referrer-Policy`, COOP/CORP und `Permissions-Policy`.
- **Keine externen Ressourcen:**
  - Schriften kommen über `@fontsource-variable/*` (Inter, JetBrains Mono).
  - Icons kommen über `@lucide/svelte`.
  - Die inlang-Plugins sind als npm-Pakete installiert und über lokale Pfade in `project.inlang/settings.json` eingebunden.
- **Absicherung durch Tests:**
  - Ein E2E-Wächter (`web/tests/e2e/fixtures.ts`) blockiert jeden Request an einen fremden Host und lässt den Test fehlschlagen; dasselbe gilt für jede Konsolenmeldung und jede CSP-Verletzung.
  - Die E2E-Tests laufen gegen die echte Server-Binary mit eingebetteter UI.
- **npm-Supply-Chain** (`web/pnpm-workspace.yaml`):
  - `minimumReleaseAge: 4320`: nur Releases, die mindestens 3 Tage alt sind.
  - Exakte Versionen in `package.json` und ein committetes Lockfile.
  - Install-Skripte sind nur per Allowlist erlaubt.

## Konsequenzen

- **Positiv:**
  - Eine Binary enthält die komplette UI (ca. 7 MiB).
  - Die UI funktioniert ohne Internetzugang.
  - Die CSP enthält kein `unsafe-inline` und keine fremden Origins.
  - Verstöße werden automatisch erkannt.
- **Negativ / Risiken:**
  - Ein Update von SvelteKit oder Svelte kann neue Inline-Styles bzw. -Skripte mitbringen. Weil der Server die Hashes aus dem Build berechnet, ist das abgedeckt; der E2E-Wächter erkennt alles andere.
  - Die Erkennung von Style-Attributen arbeitet mit einem regulären Ausdruck über die Bundles. Ungewöhnliche Schreibweisen (z. B. einfache Anführungszeichen) würden nicht erkannt. Die Folge wäre eine sichtbare CSP-Verletzung in E2E, keine Sicherheitslücke.
  - Die 3-Tage-Regel verzögert auch dringende Sicherheitsupdates von npm-Paketen. Bei Bedarf wird eine gezielte, begründete Ausnahme in `minimumReleaseAgeExclude` eingetragen.
- **Security-Auswirkungen:**
  - Weniger XSS-Fläche.
  - Keine Abhängigkeit von fremden Servern zur Laufzeit oder beim Build.
  - Kleineres Risiko durch frisch kompromittierte npm-Releases.
