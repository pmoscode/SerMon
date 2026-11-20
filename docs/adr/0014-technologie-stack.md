# 0014 – Technologie-Stack: Rust-Agent, Go-Server, SvelteKit-UI, Connect-RPC

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D1, D2, D3, D26), §4, §5; ADR 0002, 0003, 0004

## Kontext

SerMon besteht aus drei Teilen mit sehr unterschiedlichen Anforderungen:

- Der **Agent** läuft auf jedem Node, oft auf kleinen VMs, als Dauerprozess mit Zugriff auf Systemdaten. Er muss sicher
  und sparsam sein (Ziel < 30 MB RSS, < 1 % eines Kerns) und auf allen Distributionen ohne Abhängigkeiten laufen.
- Der **Server** nimmt Datenströme vieler Agents an, wertet Regeln aus und bedient die UI. Er soll als eine Binary
  ausgeliefert werden.
- Die **UI** zeigt viele Live-Charts und soll klein, schnell und ohne externe Ressourcen auskommen (D27).

Alle drei sprechen über typisierte Schnittstellen miteinander, die sich gemeinsam weiterentwickeln.

## Optionen

1. **Alles in Go** (Agent, Server): ein Ökosystem, aber Garbage Collector und größere Binary im Agent; keine
   Speichersicherheit ohne Laufzeit.
2. **Agent in Rust, Server in Go:** Speichersicherheit und kleiner Footprint auf den Nodes, einfache Nebenläufigkeit und
   reifes Netzwerk-Ökosystem auf dem Server. Zwei Sprachen im Team.
3. **Server in Rust:** eine Sprache, aber langsamere Entwicklung für Web-, Auth- und DB-Logik.
4. **UI:** React/Next (großes Ökosystem, größere Bundles), **SvelteKit als SPA** (kleine Bundles, wenig Boilerplate),
   serverseitiges Rendering (zusätzlicher Node-Prozess).
5. **Protokoll:** REST/JSON mit OpenAPI, gRPC nur für Agents, oder **Connect-RPC/Protobuf** für beide Wege.

## Entscheidung

- **D1 – Agent in Rust**, statisch gegen musl gelinkt, `tokio`, `tonic`, `rustls` (kein OpenSSL). Build mit
  cargo-zigbuild (ADR 0002).
- **D2 – Server in Go**: `net/http` aus der Standardbibliothek, `connectrpc.com/connect`, `pgx`/`sqlc`, hexagonale
  Struktur (`domain` → `app` → `adapters` → `transport`).
- **D3 – UI mit SvelteKit als SPA** (`adapter-static`), Svelte 5, Tailwind, shadcn-svelte, uPlot; eingebettet in die
  Server-Binary per `embed.FS` (ADR 0003).
- **D26 – Connect-RPC/Protobuf** als ein Schema für Agent ↔ Server (gRPC über HTTP/2, mTLS) und Web ↔ Server
  (Connect-Protokoll im Browser). Code wird mit `buf` lokal generiert und eingecheckt (ADR 0004).

## Konsequenzen

- Positiv: Eine statische Agent-Binary für alle Distributionen und beide Architekturen; ein Server-Binary inklusive UI;
  Schnittstellen sind von Ende zu Ende typisiert, Brüche fallen in `buf breaking` auf.
- Negativ: Drei Toolchains (Rust, Go, Node) in CI und Entwicklung; `just` bündelt die Befehle, das Devcontainer-Image
  bringt alles mit.
- Security: Speichersicherheit im privilegiertesten Teil (Agent, Helper); rustls statt OpenSSL verkleinert die
  Angriffsfläche; die UI enthält keinen serverseitig ausgeführten Code.
- Folgearbeiten: Versionen exakt pinnen, Releases mindestens 3 Tage alt (ADR 0006).
