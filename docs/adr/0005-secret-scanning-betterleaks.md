# 0005 – Secret-Scanning: betterleaks statt gitleaks

- Status: accepted
- Datum: 2026-11-19
- Bezug: KONZEPT.md §19.3 (Git-Hooks), §21 (security.yml); IMPLEMENTIERUNGSPLAN M0-01, M0-06

## Kontext

Seit M0-01 prüft gitleaks die gestageten Änderungen vor jedem Commit (lefthook) und die gesamte Git-Historie (`just security-secrets`).
Bei der Prüfung vor M0-06 (2026-11-19) hat sich der Stand von gitleaks als Problem herausgestellt.

Die README von gitleaks sagt dazu:

> „Gitleaks is feature complete. I'm not merging new features into Gitleaks. Future releases will be security patches only."
> „I'm shifting my focus to Betterleaks"

Weitere Fakten:

- Der Autor hat die administrative Kontrolle über das gitleaks-Repo verloren.
- Das letzte gitleaks-Release, v8.30.1, ist vom März 2026.
- **betterleaks** stammt vom ursprünglichen gitleaks-Autor (Zach Rice), wird von Aikido Security gesponsert und steht unter MIT-Lizenz.
- betterleaks wird aktiv gepflegt (v1.10.1 vom 2026-11-12) und versteht sich als Nachfolger, der gitleaks direkt ersetzt.
- Laut Berichten erkennt betterleaks mehr: u. a. kodierte Secrets, und Regeln können mit CEL validiert werden.

## Optionen

1. **Bei gitleaks bleiben.**
   Funktioniert, bekommt aber keine neuen Erkennungsregeln mehr; neue Secret-Formate werden also nicht erkannt.
   Die Zukunft des Projekts ist unklar.
2. **betterleaks** (gewählt).
   Gleicher Autor, aktive Entwicklung, gleiche Bedienung (`git --pre-commit --staged`, `--redact`).
   Es versteht `.gitleaks.toml`, `.gitleaksignore` und `gitleaks:allow`; ein späterer Wechsel des Konfigurationsformats entfällt damit.
3. **Andere Scanner** (z. B. TruffleHog).
   Ebenfalls gut, aber anders zu bedienen. Die Validierung gegen Anbieter-APIs ist dort standardmäßig ein zentraler Teil; das wollen wir nicht.

## Entscheidung

Wir nutzen **betterleaks** für den Pre-Commit-Hook (`lefthook.yml`), für den Scan der Historie (`just security-secrets`) und später in CI (M0-06).

**Live-Validierung bleibt aus.**
`--validation` würde gefundene Secrets an die APIs der Anbieter schicken, z. B. an `api.github.com`.
Ein gefundenes Secret gilt immer als kompromittiert und wird rotiert; dafür braucht es keine Prüfung bei Dritten.

**Ergebnis der Prüfung** (lokal, betterleaks 1.10.1):

- Die Git-Historie ist sauber (ca. 500 KB in 0,3 s).
- Ein gestagetes, realistisches Test-Token wird erkannt, und der Hook blockiert den Commit.

## Konsequenzen

- **Positiv:**
  - Aktiv gepflegte Erkennungsregeln.
  - Gleiche Arbeitsweise wie vorher.
  - Bestehende gitleaks-Konfigurationen bleiben nutzbar.
- **Negativ / Risiken:**
  - Ein jüngeres Projekt mit kleinerer Nutzerbasis (ca. 3.000 GitHub-Sterne gegenüber 31.000 bei gitleaks).
  - Ein Hauptsponsor. Sollte betterleaks eingestellt werden, ist der Rückweg zu gitleaks einfach, weil die Formate kompatibel sind.
- **Security-Auswirkungen:**
  - Bessere Erkennung.
  - Keine Weitergabe von Funden an Dritte, weil die Validierung aus ist.
  - In CI wird die Binary später mit gepinnter Version und geprüfter Prüfsumme installiert (M0-06).
