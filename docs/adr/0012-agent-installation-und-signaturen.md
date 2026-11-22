# 0012 – Agent-Installation: Skript, Signaturen und gehärtete Unit

- Status: accepted
- Datum: 2026-11-20
- Bezug: KONZEPT.md §6, §18.1 (Tampering), §18.4; IMPLEMENTIERUNGSPLAN M1-27; ADR 0010

## Kontext

KONZEPT §6 sieht einen Einzeiler `curl … | sh` vor. Das Skript soll die Agent-Binary samt Signatur laden und gegen
einen **im Skript eingebetteten Public Key** prüfen (minisign/cosign), den User `sermon` anlegen, die Unit installieren
und `enroll` aufrufen. Zu entscheiden war, womit Hosts die Signatur prüfen, woher Schlüssel und Binaries kommen und
wie die Unit gehärtet wird.

## Entscheidung

- **Signaturformat minisign, Algorithmus „Ed“** (Ed25519 über die Datei, nicht vorab gehasht). Erzeugt wird es mit
  reinem OpenSSL (`scripts/release-sign.sh`: Schlüssel erzeugen, Public Key ausgeben, signieren); die Key-ID sind die
  ersten 8 Bytes von SHA-256 über den Public Key. `minisign -V` prüft die Dateien (Test in `packaging/test`).
- **Prüfung im Skript:** mit `minisign`, falls installiert, sonst mit `openssl` ≥ 3.0 (auf Debian, Ubuntu, RHEL und
  Alpine durch `ca-certificates` praktisch immer vorhanden). Geprüft werden Algorithmus, Key-ID und die
  Ed25519-Signatur. Ohne beide Werkzeuge, mit fremdem Schlüssel oder veränderter Datei bricht das Skript ab, bevor
  etwas installiert ist. Es gibt keinen Schalter, der die Prüfung abschaltet.
- **Der Public Key wird in den Server kompiliert** (`-X main.releaseKey`, aus `SERMON_RELEASE_KEY` oder
  `packaging/release.pub`), nicht zur Laufzeit konfiguriert. Ohne Key liefert der Server das Skript mit Platzhalter
  aus, und es verweigert die Installation. Den privaten Schlüssel hält nur die Release-Pipeline.
- **Auslieferung durch den Server:** `GET /install.sh` (als Text, damit „Skript erst ansehen“ im Browser funktioniert)
  und `GET /downloads/sermon-agent-linux-{amd64,arm64}[.minisig]` aus `SERMON_AGENT_DOWNLOADS_DIR`. Nur diese
  Dateinamen, gelesen über `os.Root` (Symlinks aus dem Verzeichnis heraus werden nicht verfolgt).
- **TLS beim Download:** Mit `--ca-fingerprint` (selbstsigniertes Server-Zertifikat) lädt das Skript ohne
  Zertifikatsprüfung; die Integrität sichert die Signatur, `enroll` prüft danach den Fingerprint (ADR 0010).
  **Nachtrag (M1-37):** Das reichte nicht. Die Signatur war nicht an Datei und Version gebunden (ein Angreifer im Netz
  konnte eine ältere signierte Binary oder eine andere signierte Datei liefern), und das Skript selbst kam mit
  `curl -k` ungeprüft an. Siehe „Nachtrag M1-37“ unten.
- **Unit:** KONZEPT §18.4, ergänzt um `ProtectKernelLogs`, `ProtectProc=default`, `UMask=0077`, `RemoveIPC`,
  `KeyringMode=private`, `DevicePolicy=closed`, `SystemCallErrorNumber=EPERM`, `TasksMax=64`, `StateDirectory`.
  `ProtectHome=read-only` statt `yes`, damit der Dateisystem-Collector `/home` sieht. Kein `PrivateUsers`, weil sonst
  die Gruppen `systemd-journal`/`adm` für das spätere Log-Shipping wirkungslos würden. Score laut
  `systemd-analyze security --offline`: **1,6** (Ziel < 2,0, als Test mit `--threshold=20`).
- Unit und OpenRC-Skript liegen einmal unter `packaging/`; das Skript bekommt sie beim Ausliefern eingesetzt, eine
  Kopie im Server prüft `just check-gen`.

## Alternativen

- **cosign:** auf den Ziel-Hosts nicht verbreitet, große Binary; für Container-Images bleibt es vorgesehen.
- **Prüfung ohne Werkzeug** (z. B. nur SHA-256 aus einer Datei vom Server): schützt nicht gegen einen manipulierten
  Download-Server bzw. Mirror. Verworfen.
- **minisign-Prehash („ED“, BLAKE2b-512):** OpenSSL kann es nicht in einem Schritt prüfen; die Binaries sind klein
  genug für „Ed“.

## Folgen

- Der Release-Schlüssel muss erzeugt und als Secret der Release-Pipeline hinterlegt werden; `packaging/release.pub`
  kommt dann ins Repository. Bis dahin bauen Entwicklung und E2E mit einem eigenen Testschlüssel.
- Wer den Server kontrolliert, kann ein anderes Skript ausliefern – das gilt für jedes `curl | sh`. Dafür bleiben
  „Skript erst ansehen“ und das Paket-Repo (später) als Alternativen (KONZEPT O5).

## Nachtrag M1-37 (Security-Review M1-35)

- **Signatur an Datei und Version gebunden.** `release-sign.sh sign KEY --version V` schreibt in den Trusted Comment
  die durch Tabs getrennten Felder `timestamp:…`, `file:<Name>`, `hashed:0` und `version:V`; die globale Signatur
  des minisign-Formats deckt ihn ab.
  - Der Server setzt seine Version beim Ausliefern in `install.sh` ein (`AGENT_VERSION`).
  - Das Skript prüft den Trusted Comment auf beiden Wegen (minisign prüft die globale Signatur selbst, OpenSSL prüft
    sie zusätzlich) und verlangt `file:sermon-agent-linux-<arch>` sowie `version:` = Version des Servers.
  - Ein älteres signiertes Release (Rollback) oder eine andere signierte Datei unter dem Namen des Agents wird
    abgelehnt.
  - Folge: Server und Agent-Binaries im Download-Verzeichnis haben immer dieselbe Version
    (`just sign-agent KEY VERSION`); passt sie nicht, nennt das Skript beide Versionen.
- **Bootstrap ohne öffentliches Zertifikat über eine Prüfsumme.** Der Server schickt mit `install.sh` die SHA-256 der
  unkomprimierten Bytes im Header `X-Sermon-Script-SHA256`. Die UI liest ihn über die Verbindung des Browsers, der
  der Admin ohnehin vertraut, und zeigt einen Befehl, der das Skript mit `curl -k` lädt und mit `sha256sum -c` prüft,
  bevor `sudo` es ausführt. Ein Angreifer im Netz des Hosts kann so kein eigenes Skript unterschieben.
  - **Verworfen: `curl --pinnedpubkey`** (Pin auf den Schlüssel des Leaf-Zertifikats). Caddy `tls internal` tauscht das
    Leaf alle 12 Stunden, ein Befehl mit Pin wäre bald ungültig; der Server kennt das Zertifikat des Proxys zudem nicht.
    Mit `wget` gäbe es gar keinen Pin. Die Prüfsumme hängt nur am Skript und funktioniert mit jedem Download-Werkzeug.
  - **Verworfen: `Repr-Digest`** (RFC 9530) statt eines eigenen Headers: Er beschreibt die Repräsentation einschließlich
    einer Komprimierung, die ein Proxy hinzufügt; gemeint sind hier die Bytes, die `curl` speichert.
- **Token nie in der Kommandozeile.** Der Befehl der UI exportiert `SERMON_ENROLLMENT_TOKEN` (mit Leerzeichen am Anfang,
  damit bash ihn nicht in die History schreibt) und reicht es mit `sudo --preserve-env=…` weiter. `install.sh` liest
  es aus der Variable oder mit `--token-file`; `--token` funktioniert weiter, warnt aber, weil Argumente in der
  Prozessliste für alle lokalen User sichtbar sind.

## Nachtrag M2-21 (signierte Pakete)

Neben `install.sh` gibt es Pakete (`just package-agent VERSION`, nfpm): `.deb`, `.rpm`, `.apk` für amd64 und arm64.

- **Signaturen:** OpenPGP für `.deb` (Typ `origin`, `_gpgorigin`) und `.rpm`, RSA für `.apk` (Schlüsselname
  `sermon-agent`). Diese Schlüssel sind von minisign getrennt: Paketmanager kennen nur ihre eigenen Formate. Ohne
  Schlüssel baut das Skript nicht. Prüfen: `rpm --import` + `rpm -K`, `apk` mit dem Schlüssel in `/etc/apk/keys` (sonst
  verweigert es die Installation), bei `.deb` `gpg --verify _gpgorigin` (dpkg selbst prüft nicht; ein signiertes
  APT-Repo ist Folgearbeit).
- **Inhalt:** `/usr/bin/sermon-agent` (nicht `/usr/local`), Unit bzw. OpenRC-Skript mit diesem Pfad,
  `/etc/sermon/agent.toml` als Konfiguration mit Platzhalter. Das Paket legt User, Zustandsverzeichnis und Rechte an wie
  `install.sh` und eine `machine-id`, wo keine ist; es enrollt nicht und startet den Agent nicht.
- **Upgrade und Entfernen:** Nach einem Upgrade startet ein laufender Agent neu. Beim Entfernen stoppen die Skripte den
  Dienst und löschen `/var/lib/sermon-agent` (Identität, Puffer, Positionen), nicht aber bei einem Upgrade (deb
  `upgrade`, rpm `1`). Konfiguration und der User `sermon` bleiben, wie Distributionen es mit System-Usern halten
  (`.rpm`/`.apk` entfernen eine unveränderte Konfiguration).
