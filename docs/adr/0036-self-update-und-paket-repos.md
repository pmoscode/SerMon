# 0036 – Self-Update des Agents und signierte Paket-Repos

- Status: accepted
- Datum: 2026-11-25
- Bezug: KONZEPT.md §6, §14.3, O5; ADR 0012 (Signaturen, `install.sh`), ADR 0035 (Root-Helper); IMPLEMENTIERUNGSPLAN
  M4-17 bis M4-20; Threat Model A4, T40

## Kontext

Per Skript installierte Agents (`/usr/local/bin/sermon-agent`, ADR 0012) bekommen neue Versionen bisher nur, wenn jemand
`install.sh` erneut ausführt. Per Paket installierte aktualisiert der Paketmanager, sobald ein Repo mit SerMon-Paketen
eingerichtet ist – das gibt es noch nicht (KONZEPT O5).

Der Agent läuft ohne root und kann seine Binary nicht selbst ersetzen; das tut der Helper (`SelfUpdate`, ADR 0035).
Damit wird der Weg zum gefährlichsten des Helpers: Wer ihn dazu bringt, eine beliebige Datei zu installieren, hat root
auf dem Node, und das auf jedem Node der Flotte (T40). Der einzige Anker ist der Release-Schlüssel (A4).

Offen waren:

- woher Agent und Helper die Binary bekommen und wie der Helper sie prüft, ohne dass der Agent sie zwischen Prüfung und
  Einsatz tauscht;
- woran der Helper erkennt, dass die neue Version läuft, und wann er zurückrollt;
- woher der Server die Versionen je Kanal kennt;
- Schlüssel, Ablage, Rotation und Auslieferung der Paket-Repos.

## Optionen

**Download der Binary**

1. **Der Helper lädt selbst** (HTTPS zum Server): Der Helper bräuchte Netz, TLS, das Vertrauen in den Server (CA,
   Fingerprint) und einen HTTP-Client – viel Code als root (ADR 0035 hält ihn bei < 1 500 Zeilen).
2. **Der Agent lädt über `/downloads` des Web-Servers:** ein zweiter Weg mit eigener TLS-Prüfung (selbstsignierte
   Server, ADR 0010) und einem HTTP-Client im Agent, den er bisher nicht braucht.
3. **Der Agent lädt über seine Verbindung zum Agent-Endpunkt** (mTLS, `tonic`): ein Server-Stream im
   `AgentService`. Vertrauen und Client gibt es schon; der Server weiß, welcher Node lädt.

**Woran der Helper erkennt, dass die neue Version läuft**

1. **Der Helper fragt den Server:** Netz im Helper, siehe oben.
2. **Der Prozess läuft noch nach 2 min:** sagt nichts über die Verbindung zum Server; eine Version, die startet, aber
   nie verbindet, bliebe.
3. **Die neue Version bestätigt** über den Socket des Helpers, sobald ihre erste Session steht (`ConfirmSelfUpdate`).
   Ein lügender Agent kann bestätigen oder schweigen – das Ergebnis ist in beiden Fällen eine von zwei signierten
   Fassungen.

**Versionen je Kanal**

1. **Eine Liste im Server** (Konfiguration oder Datenbank), gepflegt vom Admin: Fehlerquelle, und die Dateien müssen
   trotzdem irgendwo liegen.
2. **Ein Update-Feed im Internet:** widerspricht dem Betrieb ohne Internet (self-hosted, Air-Gap).
3. **Das Download-Verzeichnis des Servers** (`SERMON_AGENT_DOWNLOADS_DIR`): Was dort mit gültiger Signatur liegt, ist
   verfügbar; die Version steht im signierten Trusted Comment (ADR 0012, Nachtrag M1-37).

**Paket-Repos**

1. **Nur ein öffentliches Repo** (etwa GitHub Pages): verlangt Internet auf jedem Node.
2. **Der SerMon-Server liefert sie aus** (`/repo/…`, statische Dateien wie `/downloads`), ein öffentlicher Spiegel
   ist derselbe Baum.

## Entscheidung

Gewählt: **Download über die Agent-Verbindung, Prüfung auf einer root-eigenen Kopie, Bestätigung durch die neue
Version, Kanäle aus dem Download-Verzeichnis, Repos vom Server.**

### Ablauf des Self-Updates

1. Der Server schickt `SelfUpdate{run_id, version}` (M4-05). Der Agent lädt Binary und Signatur über
   `AgentService.FetchAgentRelease{version, arch}` – ein Server-Stream: zuerst Größe und Signatur (`.minisig`), dann
   Blöcke zu 256 KiB, höchstens 64 MiB. Der Server liefert nur Versionen, die er selbst als gültig signiert kennt
   (siehe Kanäle). Der Agent legt beides in seinem Zustandsverzeichnis ab
   (`/var/lib/sermon-agent/self-update/sermon-agent{,.minisig}`) und ruft den Helper mit
   `SelfUpdate{run_id, version}`.
2. **Der Helper prüft auf einer eigenen Kopie** (gegen TOCTOU): Er öffnet die Dateien ohne Symlinks zu folgen
   (`openat` mit `O_NOFOLLOW` ab dem Verzeichnis), verlangt reguläre Dateien bis 64 MiB bzw. 4 KiB und kopiert sie nach
   `/var/lib/sermon-helper/runs/<run_id>/` (root, `0700`). Nur diese Kopie wird geprüft und später eingesetzt; was der
   Agent danach an seinen Dateien ändert, wirkt nicht mehr.
3. **Signatur:** minisign „Ed“ wie `install.sh` (ADR 0012): Algorithmus, Key-ID, Ed25519 über die Datei und die globale
   Signatur über den Trusted Comment. Der Comment muss `file:sermon-agent-linux-<arch>` (Architektur des Helpers) und
   `version:<V>` mit `V` = angefragte Version enthalten. Der Public Key ist **in den Helper kompiliert** (aus
   `SERMON_RELEASE_KEY` bzw. `packaging/release.pub`, wie im Server); der Helper nimmt bis zu zwei Schlüssel an (für
   die Rotation). Ed25519 kommt aus `ring` (schon im Workspace, Nachtrag M4-18), Base64 selbst.
4. **Kein Downgrade:** `V` muss nach SemVer größer sein als die installierte Version. Die kennt der Helper aus der
   Signatur der installierten Binary, die `install.sh` und jedes Self-Update root-eigen ablegen
   (`/var/lib/sermon-helper/agent.minisig`, beim Lesen erneut geprüft). Fehlt sie (Installation vor M4-18), lehnt der
   Helper ab: „`install.sh` erneut ausführen“. Was der Agent über seine Version sagt, zählt nicht.
5. **Nur Skript-Installationen:** Ersetzt wird ausschließlich `/usr/local/bin/sermon-agent`. Gibt es die Datei nicht
   (Installation per Paket nach `/usr/bin`), antwortet der Helper `UNSUPPORTED` – Pakete aktualisiert der Paketmanager
   (KONZEPT §14.3). Der Agent meldet seine Installationsart im Hello (`install_method`: Skript oder Paket, aus dem Pfad
   seiner Binary), damit der Server das Self-Update nur Skript-Installationen anbietet.
6. **Ersetzen** im eigenen Lauf (eine Unit wie jeder Lauf, ADR 0035): die alte Binary samt Signatur nach
   `/var/lib/sermon-helper/self-update/previous{,.minisig}`, die neue als `/usr/local/bin/.sermon-agent.new`
   (`root:root 0755`, `fsync`) und per `rename` über die alte, dann `fsync` des Verzeichnisses. Danach
   `systemctl restart sermon-agent` bzw. `rc-service sermon-agent restart`.
7. **Bestätigung und Rollback:** Die neue Version ruft `ConfirmSelfUpdate{run_id}`, sobald ihre erste Session steht
   (der Server hat das Hello beantwortet); sie findet den Lauf in ihrem Laufbuch (M4-12). Der Helper nimmt die
   Bestätigung nur nach dem Neustart des Laufs an. Kommt sie nicht binnen **2 min**, legt der Lauf die vorige Binary
   samt Signatur per `rename` zurück, startet den Agent neu und endet mit Fehler („zurückgerollt“); sonst endet er mit
   Exit-Code 0. Das Ergebnis liest der Agent wie bei jedem Lauf mit `RunResult` – nach einem Rollback die alte Version.
8. **Veto:** `allow_self_update` in `helper.toml` (ab Werk an, ADR 0035). Höchstens ein schreibender Lauf zur Zeit
   (dieselbe Sperre wie Updates).

### Kanäle

- `stable` ist die Version des Servers selbst: die Dateien direkt in `SERMON_AGENT_DOWNLOADS_DIR`, wie für
  `install.sh` (dort muss die Version schon heute gleich sein). Agents werden so nie neuer als ihr Server.
- `beta` ist optional: dieselben Dateinamen in `SERMON_AGENT_DOWNLOADS_DIR/beta/`, eine neuere Version. Ein Agent im
  Beta-Kanal darf neuer sein als sein Server; das Protokoll trägt das (unbekannte Nachrichten und Felder ignorieren
  beide Seiten, M4-05).
- Der Server liest beim Start und alle 5 min die Signaturen, prüft sie mit dem Release-Key und entnimmt Version und
  Architektur dem Trusted Comment. Was nicht passt, ignoriert er und protokolliert es. Neue Beta-Versionen brauchen so
  keinen Neustart.
- Der Kanal gilt je Gruppe (M4-19), Standard `stable`. Ausgelöst wird von Hand (Operator, Step-up) oder per Policy
  (`agentUpdate: auto`) im Fenster über den Orchestrator mit `maxParallel`. Ringe (gestaffelt über Gruppen) folgen
  später.

### Paket-Repos

- **Schlüssel:** ein OpenPGP-Schlüssel (RSA 4096, nur Signieren) für apt (`InRelease`) und rpm (`repomd.xml` und die
  Pakete), ein RSA-Schlüssel (4096) für apk (`APKINDEX`, Name `sermon-agent.rsa.pub`). Die privaten Schlüssel liegen wie
  der Release-Key nur als Secrets der Release-Pipeline; die öffentlichen in `packaging/keys/` und unter
  `/repo/keys/`.
- **Bau:** `scripts/build-repos.sh` (aus `deploy/e2e/repos/build.sh` von M4-08) baut aus `dist/packages` die drei Repos
  mit den letzten drei Versionen. Das Ergebnis ist ein Release-Artefakt (`sermon-repo-<version>.tar.gz`).
- **Auslieferung:** Der Server liefert es unter `/repo/{apt,rpm,apk,keys}/` aus `SERMON_REPO_DIR` aus, nur lesend
  über `os.Root` wie `/downloads`. Nodes ohne Internet hängen so am eigenen Server; ein öffentlicher Spiegel ist
  derselbe Baum. `install.sh --repo` richtet Quelle und Schlüssel ein und installiert die Pakete (O5).
- **Rotation:** Der neue Schlüssel wird ein Release lang zusätzlich ausgeliefert und von `install.sh --repo` mit
  eingerichtet (apt: beide in der Keyring-Datei von `signed-by`, rpm: beide in `gpgkey`, apk: beide unter
  `/etc/apk/keys`). Ab dem nächsten Release signiert nur noch der neue; der alte fällt ein Release später weg. Für den
  Release-Key des Self-Updates gilt dasselbe über die zwei Schlüssel im Helper; der Helper selbst kommt neu über den
  Paketmanager oder `install.sh`, nie über das Self-Update.

## Folgen

- **Grenzen von T40:** Ein kompromittierter Server kann nur eine signierte, neuere Version des Agents installieren
  lassen – nie eine fremde Datei, keine ältere (außer dem Rollback auf die gerade ersetzte) und nichts auf Hosts mit
  `allow_self_update = false`. Wer den Release-Key hat (A4), hat root auf allen Nodes mit Self-Update; das war mit
  `install.sh` schon so und bleibt die zentrale Annahme.
- **Der Helper wächst** um Kopieren, Prüfen der Signatur und Rollback. Die Grenze aus ADR 0035 steigt von 1 500 auf
  **1 800 Zeilen** und bleibt in CI geprüft. Neu im Helper ist die Abhängigkeit `ring` (Nachtrag M4-18).
- **Neue Operation des Helpers:** `ConfirmSelfUpdate{run_id}`; `SelfUpdate` bekommt das Feld `version`. Beides ist
  additiv (`buf breaking`). Ein Helper vor M4-18 antwortet `UNSUPPORTED`.
- **Neues RPC** `AgentService.FetchAgentRelease` (mTLS, Org und Node aus dem Zertifikat); Downloads werden je Node
  begrenzt (höchstens einer zur Zeit, 10 je Stunde).
- **Hello** trägt `install_method`.
- `install.sh` legt ab M4-18 die Signatur der installierten Binary für den Helper ab.
- Die Paket-Repos sind kürzbar: Fehlt die Zeit, kommen sie mit dem Release-Prozess in v1.0 (M4-20).

## Nachtrag M4-18

- **`ring` statt `ed25519-dalek`:** `ring` ist im Workspace schon für TLS da (`rustls`) und kann Ed25519 prüfen; der
  Helper bekommt so keine neue Abhängigkeit, nur eine weitere Nutzung. Der Helper hat jetzt 1 546 Zeilen.
- **Der Agent trägt den Lauf vor dem Start ins Laufbuch ein**, mit der Kennung seines Prozesses
  (`self-update:<instance>`). Nur ein anderer Prozess – die neue Version nach dem Neustart – bestätigt ihn; der alte
  Agent bestätigt auch dann nicht, wenn er vor dem Neustart noch einmal verbindet. Ein beschäftigter Helper nimmt den
  Eintrag wieder heraus, bis der Lauf wirklich startet.
- **Ersetzt und bestätigt wird über Marken im Laufverzeichnis:** `replaced` vor dem Neustart, `confirmed` von
  `ConfirmSelfUpdate`; der Helper nimmt eine Bestätigung nur zwischen beiden und vor dem Ergebnis an.
- **Kanäle im Server:** `download.Releases` liest die Signaturen beim Start, alle 5 min und bei einer unbekannten
  Version (höchstens alle 10 s); unveränderte Dateien (Größe, Änderungszeit) werden nicht erneut geprüft.
- **E2E:** Die Version des Servers ist dort jetzt `0.0.1-e2e` (SemVer statt `e2e`), damit der Helper vergleichen kann.

## Nachtrag M4-19

- **Version des Agents:** Der Agent meldet `SERMON_VERSION` aus seinem Build, sonst die Version der Crate. Die
  Release-Pipeline baut Agent und Helper mit der Version, die sie auch signiert; sonst vergliche der Server eine andere
  Version als der Helper.
- **Kanal und `agentUpdate`** stehen in der Update-Policy der Gruppe; mit `auto` aktualisiert der Orchestrator im
  Fenster zuerst den Agent, dann die Pakete (KONZEPT §14.3).

## Nachtrag M4-20

- **Bau:** `scripts/build-repos.sh PACKAGES KEYS OUT` baut aus allen Paketen in `PACKAGES` (jede Version und
  Architektur) apt (Suite `stable`, Komponente `main`), rpm (ein Repo für beide Architekturen) und apk (`x86_64/`,
  `aarch64/`). Die Werkzeuge laufen als Container (`packaging/repo/*.Dockerfile`, auch für die Quellen von M4-08). Wie
  viele Versionen im Repo bleiben, entscheidet die Pipeline über den Inhalt von `PACKAGES`.
- **Auslieferung:** `GET /repo/{apt,rpm,apk,keys}/…` aus `SERMON_REPO_DIR` über `os.Root`; keine Listen, keine
  versteckten Dateien (etwa `.work/` des Skripts), nichts außerhalb der vier Bereiche.
- **`install.sh --repo`** holt die öffentlichen Schlüssel vom Server (dieselbe geprüfte TLS-Verbindung wie das Skript),
  richtet die Quelle ein und installiert `sermon-agent` und `sermon-helper` (ohne Recommends bzw. schwache
  Abhängigkeiten, damit `--no-helper` gilt); danach Konfiguration, Enrollment und Start wie bisher. Mit
  `--ca-fingerprint` geht es nicht: Der Paketmanager braucht ein Zertifikat, dem er vertraut.
- **dnf und apk** frischen die Quellen im Helper jetzt ohne Cache auf (`makecache --refresh`, `update --force-refresh`,
  Nachtrag M4-20 zu ADR 0035), sonst sähe ein Host neue Pakete erst nach 48 h bzw. 4 h.
- **Rotation:** Öffentliche Schlüssel des nächsten Paares in `KEYS/next/` werden mit ausgeliefert (apt und dnf in einer
  Datei, apk als zweite Datei). `Valid-Until` setzt das apt-Repo nicht: Es wird nur mit Releases neu gebaut (T101).
  Seit M4-31 erreichen sie eingerichtete Hosts über das Agent-Paket (Nachtrag unten).


## Nachtrag M4-30 (Security-Review)

- **Health-Check mit Zeiten des Servers:** Der neue Agent verbindet sich, bevor sein Helper das Ende notiert; der
  Health-Check verlangte aber eine Session nach diesem (vom Host gemeldeten) Ende – ein Self-Update ohne Pakete scheiterte
  so immer. Jetzt beginnt der Health-Check mit der Zeit des Servers, und „wieder da“ heißt eine Session nach dem Senden
  des Laufs. Start und Ende eines Laufs kappt der Server beim Empfang auf seine Zeit.
- **Schlüssel beim Bau:** `build-repos.sh` und `package-agent.sh` legen die privaten Schlüssel in ein eigenes
  Verzeichnis *neben* der Ausgabe (nie darin), machen nur `apt/ rpm/ apk/ keys/` lesbar und signieren ohne Netz.
- **Offen (M4-31):** Der Helper aktualisiert sich nicht selbst, ein neuer oder widerrufener Release-Schlüssel erreicht
  per Skript installierte Hosts erst mit `install.sh`; die Schlüssel der Paket-Repos erneuern sich auf eingerichteten
  Hosts nicht, apk erkennt Schlüssel am Dateinamen; der Kanal steht nicht im signierten Kommentar.

## Nachtrag M4-31 (Repo-Schlüssel)

- **Rotation über das Paket:** `sermon-agent` bringt die öffentlichen Repo-Schlüssel mit (aktuelle und nächste aus
  `KEYS/next/`, `SERMON_PKG_PUBLIC_KEYS` von `package-agent.sh`) und legt sie dort ab, wo `install.sh --repo` sie
  einrichtet: apt `/usr/share/keyrings/sermon-archive-keyring.asc` (`Signed-By`), dnf
  `/etc/pki/rpm-gpg/RPM-GPG-KEY-sermon` (`gpgkey`), apk `/etc/apk/keys/`. Jedes Update ersetzt sie, geprüft mit dem
  alten Schlüssel. Ablauf: Release N signiert noch mit dem alten Paar und verteilt das neue, N+1 signiert nur mit dem
  neuen, sein Paket entfernt das alte. Kein eigenes Keyring-Paket: Jeder Repo-Host hat den Agent, und wer ihn entfernt,
  entfernt auch das Vertrauen ins Repo. `scripts/test-packages.sh` spielt das über drei Releases auf Debian, Rocky und
  Alpine durch.
- **dnf fragt beim Import:** Einen neuen Schlüssel für die Metadaten importiert dnf nur nach Rückfrage; der Helper
  frischt deshalb mit `dnf -y -q makecache --refresh` auf (das Upgrade lief schon mit `-y`). Einmal importierte
  Schlüssel behält dnf (rpmdb und Repo-Keyring); ein widerrufener Schlüssel braucht auf dem Host
  `rpm -e gpg-pubkey-<id>` und `dnf clean all` (`docs/admin/installation.md`).
- **apk-Schlüssel nach Fingerprint:** `sermon-<id>.rsa.pub`, `<id>` = die ersten 16 Hex-Ziffern von SHA-256 über den
  DER-Public-Key (`scripts/repo-keys.sh`), gleich in `build-repos.sh` (APKINDEX), `package-agent.sh` (`key_name`),
  Paket und `install.sh`. Der Name bleibt über Releases und Rotation stabil; der Schlüssel in `KEYS` heißt jetzt
  `apk.rsa.pub`. `sermon.asc` enthält beide OpenPGP-Schlüssel in einem Armor-Block.
- **Signierte Schlüsselliste:** `/repo/keys/SHA256SUMS` listet alle veröffentlichten Schlüssel; das Release signiert
  sie mit dem Release-Key (`release-sign.sh`, Version des Servers). `install.sh --repo` prüft die Signatur (Datei und
  Version wie bei den Binaries) und jeden Schlüssel gegen seine Prüfsumme, auf Alpine alle apk-Schlüssel der Liste
  (auch den nächsten), nur Namen der Form `sermon-<16 hex>.rsa.pub`. Ohne Release-Key bürgt nur TLS (Warnung).
- **Vorrang vor fremden Quellen:** Die Gefahr ist, dass eine andere Quelle mit gleichnamigem, neuerem Paket den Agent
  beim unbeaufsichtigten Upgrade ersetzt; gegen böswillige Signierer hilft Pinning nicht, alle Quellen sind root.
  apt: `/etc/apt/preferences.d/sermon` (`o=SerMon,l=SerMon`, 999, auch für Spiegel); dnf: `priority=10` mit
  `includepkgs=sermon-agent sermon-helper`, damit das SerMon-Repo nichts anderes überstimmt. apk kennt keine
  Prioritäten je Paket.
- **Weiter kein `Valid-Until`:** Ein abgelaufenes Release lässt `apt-get update` und damit jeden Update-Lauf des
  Helpers scheitern, auch die Sicherheitsupdates der Distribution, sobald ein Release sich verspätet. Ein altes
  Release hält nur Updates zurück (das kann, wer das Repo ausliefert, ohnehin), apt stuft nie herab, dnf und apk haben
  kein Gegenstück.
- **Offen:** `apk update` meldet eine falsche Signatur nur als Warnung (Exit 0); erst `apk add` scheitert. Die Demo
  prüft das am gefälschten Repo.

