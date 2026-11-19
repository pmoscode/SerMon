# 0006 – CI-Pipeline und Supply Chain der Build-Werkzeuge

- Status: accepted
- Datum: 2026-11-19
- Bezug: KONZEPT.md §19.3, §21; IMPLEMENTIERUNGSPLAN M0-06; ADR 0002 bis 0005

## Kontext

CI und Security-Prüfungen laufen in GitHub Actions.
Die Pipeline ist selbst Teil der Supply Chain: Eine kompromittierte Action oder ein manipuliertes Werkzeug kann Secrets abgreifen oder Releases manipulieren.

Rahmenbedingungen (Entscheidungen des Product Owners vom 2026-11-19):

- Das Repo `pmoscode/SerMon` war zunächst privat und ist seit dem 2026-11-19 **öffentlich**.
  CodeQL, OpenSSF Scorecard und native arm64-Runner sind fest eingebaut und laufen seitdem.
- Abhängigkeiten aktualisiert **Renovate**; die GitHub-App ist installiert.
- Ein **Branch-Schutz mit PR-Pflicht** kommt erst nach der Implementierung; bis dahin wird direkt nach `main` gepusht.

## Entscheidung

### Workflows

| Workflow | Trigger | Inhalt |
|---|---|---|
| `ci.yml` | Push auf `main`, PR, manuell | shell, protocol (lint, breaking gegen `origin/main`), generated-code (`just check-gen`), agent (amd64 + nativ arm64), statischer Release-Build mit Smoke-Tests (Debian und Alpine, je Architektur), server, web, e2e (Server mit eingebetteter UI) |
| `security.yml` | Push, PR, nächtlich 03:17 UTC, manuell | CodeQL (actions, go, javascript-typescript, rust; `security-extended`), zizmor, betterleaks (gesamte Historie), Dependency-Advisories (cargo-deny, govulncheck, pnpm audit), Scorecard |

Die CI ruft dieselben `just`-Befehle auf wie die lokale Entwicklung. Was in der CI rot ist, lässt sich lokal reproduzieren.

### Härtung der Pipeline

- **Actions werden per Commit-SHA gepinnt**, mit dem Versions-Tag als Kommentar.
  Neue Versionen werden erst übernommen, wenn sie **mindestens 3 Tage alt** sind, genau wie bei npm (ADR 0003) und in Renovate.
- **Minimale Rechte:** global gilt `permissions: contents: read`. Zusätzliche Rechte bekommt ein Job nur, wenn er sie braucht, und jedes ist kommentiert.
- `persist-credentials: false` bei jedem Checkout. Es werden keine Secrets verwendet.
- **Werkzeuge außerhalb von Actions:**
  - Versionen sind im `env`-Block gepinnt und mit `# renovate:` annotiert.
  - betterleaks wird zusätzlich gegen eine festgeschriebene **SHA-256-Prüfsumme** verifiziert.
  - zig wird direkt von ziglang.org geladen und gegen eine festgeschriebene SHA-256-Prüfsumme geprüft, und zwar bei
    jedem Lauf, auch wenn das Archiv aus dem Cache kommt. `mlugg/setup-zig` läuft noch auf dem abgekündigten Node.js 20.
    Renovate aktualisiert zig nicht, weil die GitHub-Tags von zig hinter den tatsächlichen Releases zurückliegen.
  - Werkzeuge über `taiki-e/install-action` nur in Versionen, die das Manifest der gepinnten Action kennt. Sonst weicht
    die Action auf `cargo-binstall` aus, das Binaries aus weniger kontrollierten Quellen lädt. Deshalb nutzt die CI
    `cargo-deny` 0.20.2 statt des erst am 2026-11-16 erschienenen 0.21.0.
  - Annotations der Läufe (Warnungen wegen Deprecation oder Fallback) gelten als Fehler und werden behoben.
- **Prüfung der Workflows:** `just lint-ci` (actionlint + zizmor im strengsten Modus) läuft lokal und in `just lint`; in CI läuft zizmor als eigener Job.

### Renovate (`renovate.json`)

- `minimumReleaseAge: 3 days` mit `internalChecksFilter: strict`.
- Updates werden pro Ökosystem gruppiert und kommen wöchentlich (montags früh). Lockfile-Wartung läuft monatlich.
- Eigene Regex-Manager für Tool-Versionen in den Workflows und für die Rust-protoc-Plugins im `justfile`.
- Hinweise im PR-Text (`prBodyNotes`):
  - für Pakete, die einen erneuten `just gen` erfordern,
  - für die betterleaks-Prüfsumme,
  - für den temporären `minimatch`-Override.

## Konsequenzen

- **Positiv:**
  - Reproduzierbare, gehärtete Pipeline.
  - Echte arm64-Tests statt Emulation, sobald das Repo öffentlich ist.
  - Neue Advisories fallen nachts auch ohne Codeänderung auf.
- **Negativ / Risiken:**
  - Sollte das Repo wieder privat werden, schlagen CodeQL und Scorecard fehl (keine Advanced Security), und die arm64-Jobs bekommen keinen Runner.
  - SHA-Pinning und die Wartezeit verzögern auch Fixes für die Actions selbst. Bei einer akuten Lücke in einer Action wird manuell und mit Begründung aktualisiert.
- **Folgearbeiten:**
  - ✓ Erster Lauf (2026-11-19): CodeQL, Scorecard und arm64 sind grün; GitHub Private Vulnerability Reporting ist eingeschaltet.
  - Nach der Implementierung den Branch-Schutz mit PR-Pflicht und erforderlichen Status-Checks einrichten (Plan v1.0).
  - Trivy-Scan der Container-Images, sobald es Images gibt (Release-Workflow).
