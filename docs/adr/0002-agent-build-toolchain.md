# 0002 – Build-Toolchain für den Agent: cargo-zigbuild

- Status: accepted
- Datum: 2026-11-18
- Bezug: KONZEPT.md §4.1 (statische musl-Binaries), D1, D7; IMPLEMENTIERUNGSPLAN M0-02

## Kontext

Agent und root-Helper sollen als **eine statische Binary pro Architektur** (amd64, arm64) auf allen Zieldistributionen laufen:
Debian, Ubuntu, RHEL-Familie und Alpine.
Gebaut wird sowohl lokal (macOS arm64, Linux) als auch in GitHub Actions.
Gewünscht sind reproduzierbare Builds, wenige Abhängigkeiten im Build-Prozess und kein OpenSSL (TLS über rustls).

## Optionen

### 1. `cross` (Docker-basierte Cross-Compilation)

- **Vorteile:**
  - Fertige Toolchain-Images.
  - Kann Tests im Ziel-Image ausführen (QEMU).
- **Nachteile:**
  - Docker ist Pflicht.
  - Auf macOS arm64 laufen die amd64-Images emuliert, das ist langsam.
  - Die Images sind eine zusätzliche Supply-Chain-Abhängigkeit.

### 2. `cargo-zigbuild` (zig als Linker und C-Compiler)

- **Vorteile:**
  - Kein Docker nötig, ein einziges Werkzeug (zig) für alle Ziele.
  - Läuft identisch auf macOS und Linux.
  - Kompiliert auch C-Abhängigkeiten für das Ziel mit.
  - Schnell.
- **Nachteile:**
  - zig und cargo-zigbuild sind zusätzliche Werkzeuge, die gepinnt werden müssen.
  - Exotische C-Abhängigkeiten können Sonderbehandlung brauchen.

### 3. musl-Cross-Toolchains (gcc, z. B. musl-cross-make)

- **Vorteile:**
  - Klassisch und bewährt.
- **Nachteile:**
  - Eine Toolchain pro Ziel.
  - Hoher Pflegeaufwand.
  - Auf macOS umständlich.

### 4. Native Builds auf Runnern je Architektur

- **Vorteile:**
  - Keine Cross-Compilation.
  - GitHub bietet amd64- und arm64-Runner an.
- **Nachteile:**
  - Lokal nicht reproduzierbar.
  - Der musl-Build braucht trotzdem eine musl-Toolchain (`musl-tools`).

## Entscheidung

**Option 2: `cargo-zigbuild`** für Release-Builds lokal und in CI (`just build-agent`).
Die Tests laufen in CI zusätzlich nativ auf amd64- und arm64-Runnern (M0-06), damit architekturspezifische Fehler auffallen.

**Ergebnis des Spikes** (macOS arm64, Rust 1.100.0, zig 0.17.1, cargo-zigbuild 0.24.1; Skelett-Binaries ohne Abhängigkeiten):

| Ziel | Build (release, LTO) | Größe | `file` | Smoke-Test |
|---|---|---|---|---|
| `x86_64-unknown-linux-musl` | 19 s | 328 KB | statically linked, stripped | ✓ `debian:13-slim` (linux/amd64, emuliert) |
| `aarch64-unknown-linux-musl` | 22 s | 312 KB | statically linked, stripped | ✓ `alpine:3.22` (linux/arm64, nativ) |

Die Prüfung ist reproduzierbar mit `just build-agent` und `just smoke-agent`.
`build-agent` bricht ab, wenn eine Binary nicht statisch gelinkt ist.

### Festlegungen

- **Toolchain gepinnt:** über `agent/rust-toolchain.toml` (Rust 1.100.0 inkl. musl-Targets, rustfmt, clippy).
  zig und cargo-zigbuild werden in CI auf feste Versionen gepinnt (M0-06).
- **Release-Profil:** LTO, `codegen-units = 1`, Symbole entfernt.
  `panic = "unwind"` bleibt, damit Module per `catch_unwind` isoliert werden können (KONZEPT §15).
- **Lints auf Workspace-Ebene:**
  - `unsafe_code = deny`.
  - Clippy `all` als Fehler, `pedantic` als Warnung (in CI über `-D warnings` ebenfalls Fehler).
  - `unwrap`/`expect`/`panic`/`todo` sind im Produktionscode verboten, in Tests erlaubt (`clippy.toml`).
- **Abhängigkeiten:** `cargo-deny` mit einer Lizenz-Allowlist, die mit AGPL-3.0 kompatibel ist.
  Verboten sind `openssl`, `openssl-sys` und `native-tls` sowie unbekannte Registries und Git-Quellen (`agent/deny.toml`).

## Konsequenzen

- **Positiv:**
  - Ein Befehl baut beide Architekturen, ohne Docker.
  - Die Binaries laufen unabhängig von glibc bzw. musl der Zieldistribution.
  - Sehr kleine Binaries als Ausgangsbasis.
- **Negativ / Risiken:**
  - **C-Abhängigkeiten der Kryptografie:** Der Standard-Crypto-Provider von rustls ist `aws-lc-rs` und braucht beim Cross-Build CMake und einen C-Compiler.
    Mit M1-13/M1-14 (erste rustls-Nutzung) prüfen, ob `aws-lc-rs` mit zigbuild für musl sauber baut.
    Alternative ist der Provider `ring`; die Entscheidung kommt dann in ein eigenes ADR.
  - Zusätzliche Werkzeuge (zig, cargo-zigbuild) müssen in Entwicklungsumgebung und CI gepflegt werden.
- **Security-Auswirkungen:**
  - Kein OpenSSL, durch `cargo-deny` erzwungen.
  - Statische Binaries bringen ihre Abhängigkeiten selbst mit.
    Sicherheitsupdates von Bibliotheken erfordern deshalb ein neues Agent-Release; das verlangt ein zügiges Release-Verfahren (M4 Self-Update).
- **Folgearbeiten:**
  - CI-Job mit gepinnten Versionen und Smoke-Tests unter QEMU (M0-06).
  - Gegenprobe mit `aws-lc-rs` bzw. `ring` beim ersten rustls-Einsatz.

## Nachtrag 2026-11-26 (M4-32): jemalloc und `agent/tools/ar`

Der Agent allokiert in den musl-Builds über jemalloc ([ADR 0038](0038-allocator-des-agents.md)). Dessen C-Teil baut
`tikv-jemalloc-sys` mit `configure` und archiviert mit `$AR`; das `ar` von macOS legt beim Cross-Build die ELF-Objekte
nicht ab, die Bibliothek bleibt leer und der Link scheitert. `agent/.cargo/config.toml` setzt daher `AR` auf
`agent/tools/ar`: für Linux-Ziele `zig ar` (zig ist ohnehin da), sonst das `ar` des Systems. Unter Linux und in der CI
ändert sich dadurch nichts Sichtbares. `scripts/check-agent-allocator.sh` prüft nach jedem Release-Build, dass jemalloc
mit den Einstellungen in der Binary steckt.
