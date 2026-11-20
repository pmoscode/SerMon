# 0010 – Agent-Enrollment: TLS-Prüfung, Pinning und Identitätsdateien

- Status: accepted
- Datum: 2026-11-19
- Bezug: KONZEPT.md §6, §18.2; IMPLEMENTIERUNGSPLAN M1-13; ADR 0002, 0004, 0009

## Kontext

`sermon-agent enroll` spricht den öffentlichen HTTPS-Endpunkt an (ADR 0009). KONZEPT §6 verlangt die Prüfung des
Server-Zertifikats gegen die **System-CA oder einen gepinnten Fingerprint** (`--ca-fingerprint`). Danach legt der Agent
Schlüssel und Zertifikate ab, die er als User `sermon` liest; `install.sh` läuft als root.

Zu entscheiden war:

- wie der Agent TLS aufbaut (tonic bringt eine eigene TLS-Anbindung mit),
- was der Fingerprint genau festlegt,
- wie der Agent das ausgestellte Zertifikat prüft,
- wie die Identität auf der Platte liegt.

## Entscheidung

### TLS selbst aufbauen, tonic bekommt den fertigen Stream

- tonics `ClientTlsConfig` erlaubt keinen eigenen Zertifikatsprüfer. Der Agent öffnet TCP und TLS deshalb selbst
  (`tokio-rustls`) und übergibt den Stream per `Endpoint::connect_with_connector`. Dasselbe Muster nutzt die
  mTLS-Verbindung (M1-14).
- Krypto-Provider ist **ring**: kein OpenSSL, baut mit zig für musl (ADR 0002).
- System-CAs kommen aus dem Betriebssystem (`rustls-native-certs`, also `ca-certificates`), nicht aus einer
  mitgelieferten Liste.
- ALPN `h2`. Beim Enrollment ist TLS 1.2 erlaubt (öffentlicher Webserver), die Agent-API bleibt TLS 1.3.

### Bedeutung des Fingerprints

`--ca-fingerprint sha256:AB:CD:…` (Präfix und Doppelpunkte optional, Groß-/Kleinschreibung egal) muss zu einem
Zertifikat passen, **das der Server im Handshake schickt**:

- **Leaf-Zertifikat:** wird wie ein SSH-Host-Key akzeptiert, ohne Ketten- und Namensprüfung. Die Handshake-Signatur
  wird trotzdem geprüft; wer nur das Zertifikat kennt, kommt nicht durch.
- **CA-Zertifikat in der Kette** (z. B. die Intermediate-CA von Caddy `tls internal`): Diese CA ist der einzige
  Vertrauensanker; Kette, Gültigkeit und Hostname werden normal geprüft.
- Passt nichts, bricht der Agent **vor dem ersten Request** ab und nennt die Fingerprints, die der Server gezeigt hat.

Eine Root-CA, die der Server nicht mitschickt, lässt sich so nicht pinnen. Ein Dateipfad zur CA (`--ca-file`) wäre die
Erweiterung, falls das gebraucht wird.

### Prüfung des ausgestellten Zertifikats im Agent

Bevor etwas gespeichert wird, prüft der Agent:

- Das Zertifikat gehört zu **seinem** Schlüssel (SPKI-Vergleich).
- Es ist jetzt gültig, für Client-Auth und von der mitgeschickten ausstellenden CA signiert.
- Es enthält genau eine SPIFFE-ID `spiffe://sermon/org/<uuid>/node/<uuid>`, und die Node-ID stimmt mit der Antwort überein.
- Das angegebene Ablaufdatum liegt in der Zukunft.

**Die Name Constraints der Agent-CA prüft der Agent nicht.** rustls-webpki lehnt jede URI-SAN unterhalb eines
URI-Constraints ab, und die Intermediate-CA beschränkt sich auf `spiffe://sermon` (M1-07). Deshalb prüft der Agent nur
gegen die Intermediate als Anker, ohne deren Constraints. Die vollständige Kette einschließlich Constraint prüft der
Server bei jedem Handshake mit Go `x509`. Das Server-Zertifikat der Agent-API hat nur DNS- und IP-SANs; dort greift
die Einschränkung von webpki nicht.

Gefunden hat das der sprachübergreifende Test `just e2e-agent` (Rust-Binary gegen den echten Server). Er läuft
deshalb auch in CI.

### Identitätsdateien

`<state_dir>/identity/` mit Modus 0700:

| Datei | Inhalt |
|---|---|
| `agent.key` | privater Schlüssel, PKCS#8-PEM |
| `agent.crt` | Client-Zertifikat und ausstellende CA(s), PEM |
| `trust.pem` | Wurzeln für das Server-Zertifikat der Agent-API |
| `identity.toml` | Org, Node, Ablauf des Zertifikats |

- Alle Dateien haben Modus 0600. Beim Laden wird ein Schlüssel abgelehnt, den Gruppe oder andere lesen dürfen.
- Dateien gehören immer dem User, der sie schreibt. `enroll` läuft als `sermon` (`install.sh` per `runuser`); als
  root verweigert der Agent das Schreiben in ein Verzeichnis, das einem anderen User gehört oder für Gruppe/andere
  beschreibbar ist. **Nachtrag (M1-35):** Ursprünglich schrieb root und übertrug den Eigentümer per `lchown`. Das
  erlaubte einem übernommenen Agent, das Staging-Verzeichnis zwischen zwei Aufrufen gegen einen Symlink zu tauschen
  und root beim nächsten Enrollment Dateien woanders schreiben und übereignen zu lassen (root-Eskalation).
- Eine neue Identität ersetzt die alte **als Ganzes**: Sie wird in `.identity.new.<pid>` geschrieben und dann
  umbenannt. Scheitert das Enrollment, bleibt die alte Identität unverändert.
- Die letzte Pfadkomponente wird nie über einen Symlink geschrieben (`O_EXCL`, Entfernen nur des Links); gegen
  getauschte Zwischenverzeichnisse schützt die Regel oben.

### Token

Das Token kommt bevorzugt aus `SERMON_ENROLLMENT_TOKEN`, weil Argumente in der Prozessliste stehen. Es erscheint
weder in `Debug`-Ausgaben noch in Fehlermeldungen. clap würde ungültige Werte wiederholen; deshalb prüft der Agent das
Token erst nach dem Parsen.

## Konsequenzen

- **Positiv:**
  - Pinning funktioniert für selbstsignierte Zertifikate und für interne CAs mit kurzlebigen Leaf-Zertifikaten.
  - Ein fehlerhaftes oder fremdes Zertifikat landet nie auf der Platte.
  - Die Tests laufen gegen einen Fake-Server im Prozess (`sermon-testserver`) und zusätzlich gegen den echten Server.
- **Negativ:**
  - Die Kette Intermediate → Root prüft der Agent nicht selbst. Ein solcher Fehler fiele erst beim ersten
    Verbindungsaufbau auf, weil der Server dort die volle Kette prüft.
  - Eigener TLS-Connector statt tonic-TLS bedeutet etwas mehr eigenen Code.
- **Security-Auswirkungen:**
  - Ohne `--ca-fingerprint` gelten nur die System-CAs. Klartext-HTTP ist nur für Loopback erlaubt (Entwicklung, siehe
    `agent.toml`).
  - Der Schlüssel verlässt den Host nie. Die CSR enthält weder Subject noch Extensions; die Identität legt allein der
    Server fest.
