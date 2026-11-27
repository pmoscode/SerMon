# Releases machen

Für den Product Owner: einmal einrichten, dann jedes Release. Entscheidungen:
[ADR 0043](adr/0043-release-prozess.md) (mit Nachtrag V1-03), Schlüssel und Rotation:
[ADR 0036](adr/0036-self-update-und-paket-repos.md). Admins prüfen und installieren ein Release nach
[docs/admin/installation.md](admin/installation.md).

## Ablauf

1. Jeder Push auf `main` aktualisiert den **Release-PR** von release-please (`release-please.yml`): Changelog aus den
   Conventional Commits, neue Version in `.release-please-manifest.json` und `version.txt`.
2. Vor dem Merge trägt der Product Owner im Text des PRs oberhalb der zweiten Linie `---` den Abschnitt
   `### Upgrade` ein (siehe unten). Die Prüfung `release-pr` schlägt ohne ihn fehl.
3. Der Merge legt den **Draft-Release** und den Tag `vX.Y.Z` an und startet `release.yml` auf dem Tag.
4. `release.yml`: Gate (Tag, Manifest, `main`, Release-PR, Upgrade) → Build von Agent, Helper und Werkzeugen (ohne
   Secrets) → **Signier-Job in der Environment `release`: wartet auf die Freigabe des Product Owners** → Server-Image
   (amd64, arm64) mit den signierten Dateien → Push nach `ghcr.io/pmoscode/sermon-server`, cosign, SBOM →
   SLSA-Provenance → Dateien hochladen, Release veröffentlichen → `scripts/verify-release.sh` gegen den
   veröffentlichten Release.

Ohne Freigabe passiert nichts mit den Schlüsseln; abgelehnt bleibt der Draft ohne Dateien liegen.

## Einmal einrichten

Reihenfolge einhalten; jeder Schritt mit dem eigenen Konto (2FA). Erledigt und geprüft am 2026-11-27: Schritte 2
(ohne die Secrets), 5 und 6.

1. **Schlüssel erzeugen** auf einem vertrauenswürdigen Rechner mit `openssl` (≥ 3.0), `gpg` und `age` (oder Go) im
   Checkout von `main`:

   ```sh
   age-keygen -o ~/sermon-offline.age-identity        # oder: ohne --recipient fragt age nach einer Passphrase
   scripts/release-keys.sh generate \
     --recipient "$(age-keygen -y ~/sermon-offline.age-identity)" \
     --bundle ~/sermon-release-keys.tar.age \
     --set-secrets pmoscode/SerMon                      # braucht gh (angemeldet) und die Environment aus Schritt 2
   ```

   Das Skript erzeugt Release-Key (Ed25519), OpenPGP-Schlüssel (RSA 4096) und apk-Schlüssel (RSA 4096), je mit einem
   Nachfolger (`next`), nur in einem temporären Verzeichnis. Es schreibt die Public Keys nach `packaging/`
   (`release.pub`, `release-next.pub`, `keys/`, `keys/next/`), die verschlüsselte Offline-Kopie aller Schlüssel nach
   `--bundle` und – mit `--set-secrets` – die drei Secrets der Environment. Ohne `--set-secrets` nennt es die Befehle,
   die die Werte aus der Offline-Kopie in `gh secret set` leiten; die Werte erscheinen nie am Bildschirm.
   Die Environment muss vorher existieren (Schritt 2), sonst `--set-secrets` weglassen und die Befehle danach
   ausführen.
2. **Environment `release` anlegen:** Settings → Environments → New environment `release`:
   - *Required reviewers:* der Product Owner (bei einem einzigen Maintainer „Prevent self-review“ aus).
   - *Deployment branches and tags:* „Selected branches and tags“, nur die Tag-Regel `v*`.
   - *Secrets* (Schritt 1): `SERMON_RELEASE_SIGNING_KEY` (`keys/current/release.pem`), `SERMON_PKG_GPG_KEY`
     (`keys/current/gpg.asc`), `SERMON_PKG_APK_KEY` (`keys/current/apk.rsa`). Keine Repository- oder
     Organisations-Secrets dieser Namen; die Schlüssel von `next/` kommen nie zu GitHub.
3. **Offline-Kopie** (`~/sermon-release-keys.tar.age`) und die age-Identität bzw. Passphrase getrennt sichern, z. B.
   zwei USB-Sticks an zwei Orten; danach vom Rechner löschen. Prüfen, dass sie aufgeht:
   `age -d -i IDENTITÄT sermon-release-keys.tar.age | tar -tf -`.
4. **Public Keys committen:** `git add packaging/release.pub packaging/release-next.pub packaging/keys` und als
   `chore(release): add the release keys` (Footer `Refs: V1-03`) nach `main`. Ab dann bauen Server und Helper mit
   diesem Schlüssel (`just build-server`, `just release-agent`).
5. **Actions-Einstellungen:** Settings → Actions → General → *Workflow permissions*: „Read repository contents“ und
   **„Allow GitHub Actions to create and approve pull requests“** an (release-please öffnet den PR mit dem
   `GITHUB_TOKEN`).
6. **Tags schützen:** Settings → Rules → Rulesets → New tag ruleset `release tags`, Ziel `v*`: *Restrict updates* und
   *Restrict deletions* (ein veröffentlichter Tag wird nie verschoben). Das Anlegen bleibt frei, release-please legt
   die Tags an.
7. **Branch-Schutz** (V1-16): die Prüfung `release notes with a section "Upgrade"` (Workflow `release-pr`) als
   Required Check auf `main`.
8. **Nach dem ersten Release:** Das Paket `sermon-server` auf GHCR ist zunächst privat: Profil → Packages →
   `sermon-server` → Package settings → *Change visibility* → Public. Unter *Manage Actions access* muss `SerMon`
   mit „Write“ stehen (kommt über das Label `org.opencontainers.image.source`).

## Ein Release

- **Kandidat oder Release:** Jede Version ist zunächst ein Kandidat (`1.0.0-rc.1`, `-rc.2`, …; GitHub-Prerelease,
  Kanal `beta`, kein Image-Tag `X.Y`). Für den Release `X.Y.Z` in einem eigenen Commit
  `"release-as": "X.Y.Z"` in `release-please-config.json` unter `packages["."]` setzen, den Release-PR mergen und
  `release-as` danach wieder entfernen.
- **Abschnitt Upgrade** – zuletzt eintragen, release-please überschreibt den Text bei jedem Push auf `main`:

  ```markdown
  ### Upgrade

  - Breaking Changes: …
  - Neue Secrets oder Einstellungen: … (z. B. `./secrets.sh` erneut ausführen)
  - Daten, die das Upgrade löscht oder umbaut: …
  - Rollback: den vorigen Digest in `.env` (`use-release.sh` notiert ihn), Migrationen laufen nur vorwärts, also
    mit dem Backup von vor dem Upgrade ([Backup](admin/backup.md)).
  ```

  Auch „nichts zu tun“ wird hingeschrieben.
- **Freigabe:** Actions → Lauf `release` → *Review deployments* → `release` → Approve. Vorher die Jobs `gate`,
  `agent` und `tools` ansehen: Version, Commit.
- **Danach:** Der Job `verify` muss grün sein. Lokal nachprüfen: `just verify-release X.Y.Z` (braucht cosign,
  optional slsa-verifier).

**Wenn etwas scheitert:** fehlgeschlagene Jobs neu starten (*Re-run failed jobs*), solange der Release ein Draft ist.
Eine einmal veröffentlichte Version wird nie ersetzt – ist sie kaputt, folgt die nächste Version (`-rc.N+1` bzw.
`X.Y.Z+1`). Einen Lauf von Hand starten: `gh workflow run release.yml --ref vX.Y.Z`.

## Schlüssel wechseln

- **Geplant** (ADR 0036): Release N wird noch mit `current` signiert und verteilt `next` (das ist ab dem ersten
  Release so). Für Release N+1: aus der Offline-Kopie `next` als neue Secrets setzen, `packaging/release-next.pub` →
  `packaging/release.pub` und `packaging/keys/next/*` → `packaging/keys/` verschieben, mit
  `scripts/release-keys.sh generate --public NEU …` einen neuen Nachfolger erzeugen und dessen `release.pub` bzw.
  `keys/` als neues `next` übernehmen (das Skript überschreibt nie: in ein leeres Verzeichnis erzeugen und von Hand
  verschieben). Neue Offline-Kopie sichern.
- **Bei Verdacht auf Kompromittierung:** sofort wie oben auf `next` wechseln und ein Release machen, Secrets der
  Environment ersetzen, ein Security-Advisory veröffentlichen (welcher Schlüssel, ab welchem Release); dnf behält
  einen widerrufenen Schlüssel, Hinweis aus [docs/admin/installation.md](admin/installation.md) ins Advisory. Die
  Einträge im öffentlichen Transparenz-Log (Rekor) zeigen jede Signatur der Identität
  `…/release.yml@refs/tags/v…` – dort nach unbekannten Einträgen suchen.

## Lokal ausprobieren

`just release-dry-run` (Docker nötig) macht mit Schlüsseln dieses Laufs, was `release.yml` mit den Dateien macht:
Agent und Helper mit dem Test-Release-Key, `scripts/release-package.sh` (die Arbeit des Signier-Jobs), die
Offline-Prüfungen von `scripts/verify-release.sh`; mit `IMAGE=1` auch das Server-Image des Releases für die
Docker-Plattform (`sermon-server:dry-run`) samt Prüfung, dass Downloads und Repos darin liegen. Alles landet in
`dist/release-dry-run/`.
