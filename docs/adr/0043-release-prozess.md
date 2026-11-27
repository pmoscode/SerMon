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
