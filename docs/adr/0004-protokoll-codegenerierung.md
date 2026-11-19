# 0004 – Protokoll: buf mit lokalen Plugins, generierter Code im Repo

- Status: accepted
- Datum: 2026-11-19
- Bezug: KONZEPT.md D26, §5, §21; IMPLEMENTIERUNGSPLAN M0-05; Skill `sermon-proto-change`

## Kontext

Agent (Rust), Server (Go) und Web-UI (TypeScript) sprechen Protobuf über Connect/gRPC (D26).
Aus `proto/` muss Code für alle drei Sprachen entstehen.

Zu entscheiden war:

- wie generiert wird,
- ob der generierte Code ins Repo kommt,
- wie die Validierungsregeln (protovalidate) eingebunden werden.

## Optionen

### Plugins

- **Remote-Plugins der Buf Schema Registry (BSR).**
  Bequem, aber die `.proto`-Dateien gehen zur Generierung an einen fremden Dienst, und das Ergebnis hängt von dessen Verfügbarkeit ab. → abgelehnt.
- **Lokale Plugins mit gepinnten Versionen** (gewählt):

  | Sprache | Plugins | Versionsquelle |
  |---|---|---|
  | Go | `protoc-gen-go`, `protoc-gen-connect-go` | `tool`-Direktiven in `server/go.mod` (`go tool …`) |
  | TypeScript | `@bufbuild/protoc-gen-es` | exakt gepinnt in `web/package.json` |
  | Rust | `protoc-gen-prost`, `protoc-gen-tonic` 0.5.0 | `cargo install --locked` über `just install-proto-plugins`; passend zu prost/tonic 0.14 |

### Generierter Code

- **Beim Build generieren:** Jeder Build bräuchte buf und alle Plugins, auch Docker-Builds und frische Clones. → abgelehnt.
- **Committen** (gewählt): Agent, Server und UI bauen ohne buf. `just check-gen` stellt in CI sicher, dass der committete Stand exakt `just gen` entspricht.

### Validierung

- **protovalidate** (`buf.validate`-Annotationen im Schema).
  Die Abhängigkeit `buf.build/bufbuild/protovalidate` kommt aus der BSR und ist per Digest in `proto/buf.lock` gepinnt. Das entspricht einer normalen Paketabhängigkeit wie bei npm oder crates.io; es wird dabei kein eigener Code an Dritte übertragen.
- Der **Server validiert jede eingehende Nachricht** mit `buf.build/go/protovalidate`.
- Der Agent prüft Server-Befehle typisiert in Rust. Eine offizielle protovalidate-Implementierung für Rust gibt es nicht.

## Entscheidung

- `proto/buf.yaml`:
  - Lint-Regeln `STANDARD` + `COMMENTS`, d. h. jedes Element muss dokumentiert sein.
  - Breaking-Check `WIRE_JSON`.
- `proto/buf.gen.yaml`:
  - Nur lokale Plugins.
  - `clean: true`, d. h. die Zielordner werden bei jedem Lauf geleert.
  - Die Typen werden pro Sprache gefiltert: Der Agent bekommt nur `sermon.agent.v1`, die UI nur `sermon.web.v1`.
- **Zielordner** (nie von Hand ändern, keine eigenen Dateien darin ablegen):
  - `server/internal/gen`
  - `web/src/lib/api/gen`
  - `agent/crates/sermon-transport/src/gen`
- **Befehle:**
  - `just gen` – Code generieren.
  - `just lint-proto` – Lint und Formatprüfung.
  - `just breaking-proto` – Breaking-Change-Check gegen `main`.
  - `just check-gen` – Drift-Check für CI.
- **Namensregel:** RPC-Namen dürfen nicht mit Methoden der generierten Clients kollidieren.
  `Connect` kollidierte mit dem Konstruktor `connect()` des tonic-Clients; die Agent-RPC heißt deshalb `AgentService.Session`.

### connect-go v1 statt v2 (Ergänzung M1-02, 2026-11-19)

connect-go **v2** ist am 2026-10-07 erschienen und stellt die Go-API grundlegend um: keine Request-/Response-Wrapper
mehr, Server und Transport neu aufgebaut. Das Protokoll auf der Leitung bleibt gleich. Wir bleiben vorerst bei
**v1** (weiter unterstützt) und nutzen `connectrpc.com/validate` v0.7.0, die letzte Version für v1. Die Migration auf
v2 ist eine eigene Aufgabe mit dem offiziellen Werkzeug `connect-go-v2-migrate`, sobald v2 im Ökosystem gereift ist.
Renovate-PRs, die v2 einführen, werden nicht ungeprüft gemergt.

## Konsequenzen

### Server-Stubs für Agent-Tests (Ergänzung M1-13, 2026-11-19)

Agent-Tests brauchen einen gRPC-Server, der `sermon-server` spielt (Enrollment, später der Session-Stream). Dafür
erzeugt `protoc-gen-tonic` zusätzlich **nur Server-Stubs** nach `agent/crates/sermon-testserver/src/gen`
(`no_client`, `no_include`, Nachrichtentypen per `extern_path` aus `sermon-transport`).

- `sermon-testserver` ist nur Dev-Abhängigkeit und steht nicht in `default-members`. Die Release-Binaries enthalten
  weder Server-Code noch `axum`.
- Der Ordner gehört zu den generierten Zielordnern (`just check-gen`).

## Konsequenzen

- **Positiv:**
  - Reproduzierbar und offline-fähig.
  - Keine Weitergabe des Schemas an Dritte.
  - Ein frischer Clone baut ohne buf.
  - Validierungsregeln stehen an einer Stelle im Schema und sind durch Tests abgesichert (`server/internal/transport/agentapi/schema_test.go`).
- **Negativ / Risiken:**
  - Generierter Code vergrößert Diffs. Abhilfe: Commits mit Schemaänderungen enthalten Schema und generierten Code zusammen.
  - Die Rust-Plugins (0.5.0) sind etwa ein Jahr alt. Beim Upgrade von prost/tonic muss geprüft werden, ob es passende Plugin-Versionen gibt. Fallback wäre `tonic-prost-build` im `build.rs`; dafür bräuchte es dann `protoc`.
  - Validierung im Agent nur von Hand.
- **Security-Auswirkungen:**
  - Größen- und Formatgrenzen stehen im Schema und werden serverseitig für jede Nachricht erzwungen.
  - Agent-Nachrichten enthalten keine Org- oder Node-IDs; die Identität kommt nur aus dem mTLS-Zertifikat.
