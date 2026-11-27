# 0043 – Release-Prozess: Versionen, Kanäle, Schlüssel und Supply Chain

- Status: accepted
- Datum: 2026-11-27
- Bezug: KONZEPT.md §21, §22, §2 D33; ADR 0006 (CI), ADR 0012 (Signaturen), ADR 0028 (Produktions-Compose), ADR 0036
  (Self-Update und Paket-Repos); IMPLEMENTIERUNGSPLAN V1-02

## Kontext

Bis M5 baut jede Installation ihr Server-Image aus dem Quellcode (ADR 0028), Agent-Binaries und Paket-Repos legt der
Admin von Hand in `deploy/compose/downloads/` und `deploy/compose/repo/`, signiert mit Schlüsseln, die es nur als
Testschlüssel gibt. Für v1.0 braucht es veröffentlichte, signierte Artefakte, aus denen ein Admin installiert und
aktualisiert (V1-03, V1-04), und einen Release-Kandidaten für den Testbetrieb (V1-18).

Kräfte:

- **A4:** Wer den Release-Schlüssel des Self-Updates oder die Schlüssel der Paket-Repos hat, hat root auf allen Nodes
  mit Self-Update bzw. Repo-Installation. Die Schlüssel sind das wertvollste Geheimnis des Projekts.
- **Version des Servers = Version des Agents:** `install.sh` und der Helper nehmen nur eine Binary an, deren signierter
  Kommentar genau die Version des Servers nennt (ADR 0012, M1-37); der Server bietet je Kanal genau eine Version an
  (ADR 0036).
- **Einfacher Betrieb (D23):** `docker compose pull && up -d` soll genügen.
- Ein Release soll ohne Handarbeit laufen, aber nie ohne Freigabe durch den Product Owner.

## Optionen

**Registry:** (1) GHCR – Rechte wie das Repo, kein weiteres Konto, Attestations am Image; (2) Docker Hub – bekannter,
aber eigenes Konto, Token und Pull-Limits; (3) beide.

**Private Schlüssel:** (1) Secrets einer GitHub-Environment `release` mit Freigabe durch den Product Owner – Releases
ohne Handarbeit, das Risiko liegt bei GitHub und der Pipeline; (2) offline beim Product Owner signieren – höchster
Schutz, Handarbeit bei jedem Release und eigene Fehlerquelle; (3) gemischt (Release-Key offline, Repo-Schlüssel in
GitHub).

**cosign:** (1) keyless über Sigstore mit dem OIDC-Token des Workflows – kein langlebiger Schlüssel, Eintrag im
öffentlichen Transparenz-Log; (2) eigener cosign-Schlüssel.

**Versionen:** (1) release-please mit Release-PR aus den Conventional Commits; (2) Tags und Notes von Hand.

## Entscheidung

Gewählt (Product Owner, 2026-11-27): GHCR, GitHub-Environment mit Freigabe, cosign keyless, release-please.

1. **Versionen:** SemVer, **eine Version für Server, Agent und Helper**. release-please im Manifest-Modus mit einem
   Paket `sermon` erzeugt aus den Conventional Commits einen Release-PR mit Changelog; sein Merge setzt den Tag
   `vX.Y.Z`, der Tag startet `release.yml`. Release-Kandidaten sind `vX.Y.Z-rc.N` (release-please mit
   `prerelease-type: rc`), ebenfalls aus einem Release-PR. Ein Tag ohne Release-PR baut nichts.
2. **Kanäle:** Ein Release-Kandidat ist ein Prerelease auf GitHub und gehört zum Kanal `beta` (ADR 0036: Helper nur mit
   `allow_prerelease = true`); ein Release ist `stable`. Image-Tags: `X.Y.Z` und `X.Y` nur für Releases, `latest`
   gibt es nicht; ein Kandidat nur als `X.Y.Z-rc.N`. `deploy/compose` pinnt das Image per Digest (V1-04).
3. **Registry:** `ghcr.io/pmoscode/sermon-server`, öffentlich, multi-arch (amd64, arm64). Nur der Job, der das Image
   schiebt, bekommt `packages: write`.
4. **Inhalt des Images:** Das Server-Image bringt die signierten Agent- und Helper-Binaries und die Paket-Repos
   **seiner eigenen Version** mit (`/srv/downloads`, `/srv/repo`, `SERMON_AGENT_DOWNLOADS_DIR` und `SERMON_REPO_DIR`
   zeigen ab Werk dorthin). Damit passen Server und Agents nach jedem `pull` zusammen, ohne dass ein Admin Dateien
   kopiert. Ein eigenes Verzeichnis per Bind-Mount bleibt möglich (z. B. `beta/` neben `stable`). Das Image wächst um
   die Pakete (grob 60–80 MB); das ist der Preis für einen Schritt weniger im Betrieb.
5. **Schlüssel:** Release-Key (Ed25519, minisign-Format, `scripts/release-sign.sh`), OpenPGP-Schlüssel der Repos und
   apk-Schlüssel liegen **nur** als Secrets der GitHub-Environment `release`:
   - Die Environment verlangt die Freigabe des Product Owners und gilt nur für Tags `v*`.
   - Nur der Signier-Job nutzt sie. Er bekommt die gebauten Dateien als Artefakte mit ihren SHA-256 aus dem Build-Job,
     prüft sie, signiert mit den Skripten des Repos (OpenSSL, gpg ohne Netz) und hat außer `checkout` und
     `download-artifact` keine Actions Dritter. Build-Jobs sehen keine Secrets.
   - Der Product Owner hält eine mit age verschlüsselte Offline-Kopie aller privaten Schlüssel (Wiederherstellung,
     Widerruf). Die öffentlichen Teile stehen im Repo (`packaging/release.pub`, `packaging/keys/`) und werden in Server
     und Helper kompiliert bzw. ausgeliefert.
   - Rotation wie in ADR 0036 (zwei Schlüssel im Helper, `next/` bei den Repos, ein Release Überlappung); bei Verdacht
     auf Kompromittierung sofort ein Release mit dem nächsten Schlüssel und ein Security-Advisory.
6. **cosign keyless:** Image und jede Release-Datei werden über Sigstore signiert (Fulcio-Zertifikat aus dem
   OIDC-Token, Eintrag in Rekor). Geprüft wird gegen die Identität
   `https://github.com/pmoscode/SerMon/.github/workflows/release.yml@refs/tags/v…` und den Aussteller
   `https://token.actions.githubusercontent.com`. Das ergänzt die minisign-Signaturen, ersetzt sie nicht: Agents und
   Helper prüfen weiter offline gegen den einkompilierten Release-Key.
7. **SBOM:** CycloneDX über syft für das Image und die Binaries, als Release-Datei und als cosign-Attestation am Image.
8. **Provenance:** SLSA Build Level 3 über die wiederverwendbaren Workflows von `slsa-framework/slsa-github-generator`
   (generic für Dateien, container für das Image). Sie müssen per Tag referenziert werden (ihr Builder prüft die
   eigene Referenz) – die einzige Ausnahme von „Actions per SHA“ (ADR 0006), kommentiert im Workflow und in der
   Konfiguration von zizmor.
9. **Release-Notes:** aus release-please, ergänzt um einen Abschnitt „Upgrade“ (Breaking Changes, neue Secrets,
   Daten, die das Upgrade löscht, Rollback-Hinweis) – Pflicht für jeden Release-PR.

## Konsequenzen

- Positiv: Ein Release braucht nur den Merge des Release-PRs und die Freigabe der Environment; Admins installieren und
  aktualisieren mit `docker compose pull && up -d`, Agents und Repos kommen passend mit.
- Positiv: Kein langlebiger cosign-Schlüssel; jede Datei ist doppelt prüfbar (minisign offline, Sigstore online).
- Negativ / Risiken: Die Release-Schlüssel liegen bei GitHub. Ein kompromittiertes GitHub-Konto des Product Owners
  oder eine kompromittierte Action im Signier-Job hätte A4. Gegenmaßnahmen: Freigabe nur durch den Product Owner
  (2FA), Signier-Job ohne Actions Dritter, Environment nur für Tags `v*`, Branch-Schutz (V1-16), Rotation und
  Advisory-Prozess; die Offline-Kopie erlaubt den Wechsel ohne GitHub.
- Negativ: Das Image wird größer; Rekor-Einträge sind öffentlich (enthalten nur, was ohnehin veröffentlicht wird).
- Security-Auswirkungen: neues Asset „Release-Schlüssel in der GitHub-Environment“ und Vertrauensgrenze „Pipeline ↔
  GitHub“ im Threat Model (V1-03).
- Folgearbeiten: V1-03 (`release.yml`, `release-please-config.json`, Probe-Release), V1-04 (Image per Digest in
  `deploy/compose`, Signaturprüfung in der Anleitung), V1-06 (Upgrade-Test `rc.1` → `rc.2`), V1-09 (Admin-Doku).

## Nachtrag V1-03 (Release-Pipeline)

Umgesetzt in `.github/workflows/release-please.yml`, `release.yml` und `release-pr.yml`; Anleitung für den Product
Owner in [`docs/release.md`](../release.md).

- **Start auf dem Tag ohne persönlichen Token:** Tags und Releases, die release-please mit dem `GITHUB_TOKEN` anlegt,
  starten keine Workflows. `release-please.yml` startet deshalb nach dem Merge `release.yml` per `workflow_dispatch`
  auf dem neuen Tag (`gh workflow run release.yml --ref vX.Y.Z`, Recht `actions: write`). Der Lauf hat so
  `refs/tags/vX.Y.Z`: Die Environment-Regel „nur Tags `v*`“ greift, und die Identität für cosign bleibt
  `…/release.yml@refs/tags/vX.Y.Z`.
- **Draft bis zum Schluss:** release-please legt den Release als Draft an, den Tag sofort (`draft`,
  `force-tag-creation`); erst der letzte Job lädt die Dateien hoch und veröffentlicht ihn. Ein veröffentlichter Release
  wird nie geändert, eine veröffentlichte Version im Registry nie durch ein anderes Image ersetzt (ein erneuter Lauf
  mit gleichem Digest ist erlaubt).
- **Gate „kein Tag ohne Release-PR“:** Der erste Job verlangt einen Tag `vX.Y.Z` oder `vX.Y.Z-rc.N`, die Version im
  Manifest, den Commit auf `main`, einen gemergten PR von `github-actions[bot]` aus einem Zweig `release-please--…` zu
  diesem Commit und den Abschnitt „Upgrade“ in dessen Notes (`scripts/check-release-notes.sh`, auch als Prüfung
  `release-pr` am PR und noch einmal vor dem Veröffentlichen). release-please überschreibt den Text des PRs bei jedem
  Push; den Abschnitt trägt der Product Owner deshalb zuletzt ein.
- **Versionen:** `versioning: prerelease` mit `prerelease-type: rc.1`: Jede Version ist zunächst ein Kandidat
  (`1.0.0-rc.0` → `1.0.0-rc.1` → …, nach `1.0.0` → `1.0.1-rc.1`); einen Release macht `release-as: X.Y.Z` in einem
  eigenen Commit, danach wird es wieder entfernt. (`prerelease-type: rc` ergäbe nach einem Release `X.Y.Z-rc` ohne
  Nummer.) Agent und Helper bekommen die Version beim Bau (`SERMON_VERSION`, `just release-agent`), der Server über
  `-X main.version`.
- **Build-Jobs ohne Secrets und ohne Caches** (Cache-Poisoning): `agent` (musl amd64/arm64 mit der gepinnten
  Toolchain, den committeten Public Keys und der Version, beides in den Binaries geprüft), `tools` (die Images von
  nfpm und der Repo-Werkzeuge als Artefakt), `image` (Server-Image für beide Architekturen in ein OCI-Archiv, ohne
  Rechte; Web-UI und Server auf der Build-Plattform, der Server cross-kompiliert, also ohne QEMU).
- **Übergabe zwischen Jobs:** Jeder Job versiegelt sein Verzeichnis (`scripts/release-artifacts.sh`, SHA-256 aller
  Dateien), der nächste prüft das Siegel gegen den Job-Output und lehnt geänderte, fehlende und zusätzliche Dateien ab.
- **Signier-Job** (Environment `release`): außer GitHubs eigenen `checkout`, `download-artifact` und
  `upload-artifact` keine Actions (das Hochladen braucht er, um die signierten Dateien weiterzugeben). Er lädt die
  Werkzeuge aus dem Artefakt (`SERMON_NFPM_IMAGE`, `SERMON_REPO_TOOLS=loaded`, `docker run --pull never --network none`),
  schreibt die Schlüssel nur nach `$RUNNER_TEMP` (0700, danach gelöscht) und prüft sie zuerst gegen die committeten
  Public Keys (`scripts/release-keys.sh check`: Release-Key, OpenPGP-Fingerprint und Signieren ohne Passphrase,
  apk-Schlüssel). Die eigentliche Arbeit macht `scripts/release-package.sh`, lokal mit Testschlüsseln ausführbar.
- **Nachfolger von Anfang an:** `scripts/release-keys.sh generate` erzeugt zu jedem Schlüssel einen Nachfolger. Dessen
  Public Keys stehen von Beginn an im Repo (`packaging/release-next.pub` im Helper als zweiter Schlüssel,
  `packaging/keys/next/` in Paketen und Repos); die privaten nur in der Offline-Kopie. Ein Leck der Environment
  trifft den Nachfolger nicht, und ein Wechsel nach einer Kompromittierung wirkt sofort auf allen Hosts mit einem
  Release dieser Generation.
- **Image:** gebaut ohne Rechte, veröffentlicht unverändert von einem eigenen Job (`skopeo copy --all
  --preserve-digests`, `packages: write`, `id-token: write`), der keinen fremden Code ausführt; danach `cosign sign`
  und `cosign attest --type cyclonedx`. Es bringt `/srv/downloads` (Agent und Helper, signiert) und `/srv/repo`
  (apt, rpm, apk, Schlüssel) seiner Version mit; die Variablen zeigen ab Werk dorthin. **Abweichung von ADR 0036:**
  Das Repo im Image enthält nur die eigene Version, nicht die letzten drei – ein Rollback ist das vorige Image mit
  seinem Repo, apt stuft ohnehin nicht herab.
- **Dateien des Releases:** Binaries mit `.minisig`, Pakete als `sermon-{agent,helper}-VERSION-{amd64,arm64}.{deb,rpm,apk}`
  (nfpm schreibt `~` in die Namen, GitHub ändert es in Namen von Release-Dateien; im Repo bleiben die Namen von
  nfpm), `sermon-repo-VERSION.tar.gz`, SBOMs `sermon-agent-VERSION.cdx.json` (aus `Cargo.lock`; ohne
  `cargo-auditable` sieht syft in den Rust-Binaries nichts) und `sermon-server-VERSION.cdx.json` (Image),
  `SHA256SUMS` mit `.minisig`, `sermon-server-VERSION.digest`, zu jeder Datei ein Sigstore-Bundle `.sigstore.json` und
  die SLSA-Provenance `sermon-VERSION.intoto.jsonl`.
- **Kandidaten bei apk:** apk nimmt eine Vorabversion nur mit Ziffern direkt nach dem Suffix; `package-agent.sh`
  schreibt `1.0.0-rc.1` für apk als `1.0.0_rc1` (deb und rpm `1.0.0~rc.1`). `just test-packages` aktualisiert jetzt
  über einen Kandidaten auf den Release.
- **Prüfen:** `scripts/verify-release.sh VERSION` (auch `just verify-release`) prüft einen veröffentlichten Release
  nur mit den Public Keys des Checkouts: minisign wie `install.sh`, Repos (Schlüsselliste signiert, apt und rpm mit
  den committeten OpenPGP-Schlüsseln, nur Pakete des Releases), SBOMs, jedes Sigstore-Bundle gegen die Identität des
  Tags, Image-Signatur, Tag = Digest des Releases, SBOM-Attestation und mit `slsa-verifier` die Provenance; der letzte
  Job von `release.yml` führt es aus.
- **Grenzen:** Der Probe-Release steht aus, bis der Product Owner Schlüssel und Environment angelegt hat; bis dahin
  sind Lint, Skripte und Läufe mit Testschlüsseln (`just release-dry-run`) geprüft, nicht GitHub selbst. Das Paket auf GHCR ist nach dem ersten Push privat
  und muss einmal öffentlich gestellt werden. Trivy-Scans der Images (ADR 0006) folgen nicht hier.
