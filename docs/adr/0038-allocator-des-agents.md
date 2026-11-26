# 0038 – Allocator des Agents: jemalloc statt musls mallocng

- Status: accepted
- Datum: 2026-11-26
- Bezug: KONZEPT.md §4.1 (< 30 MB RSS), D1; ADR 0002 (cargo-zigbuild, statisch mit musl); IMPLEMENTIERUNGSPLAN
  M4-29, M4-32

## Kontext

Der Agent ist statisch gegen musl gebaut (D1, ADR 0002); damit allokiert Rust über musls `mallocng`. In der Demo M4-28
wuchs der eigene Speicher des Agents (`RssAnon`) über einen langen Lauf von 4 auf bis zu 26 MiB, RSS 31,9 MiB – über dem
Budget von KONZEPT §4.1. Die Untersuchung (M4-32):

- **Kein Leck:** Ein Heap-Profil (`dhat`) und ein zählender Allocator über eine ganze Demo zeigen höchstens 1,1–1,7 MiB
  lebendige Daten, am Ende 0,7–1,3 MiB. Der Agent legt aber rund 1 MiB je Sekunde an und gibt sie wieder frei (3 300
  Blöcke je Sekunde: Collectors, Log-Pipeline, Samples für den Versand).
- **mallocng behält Speicher:** Es gibt eine Gruppe erst frei, wenn alle ihre Plätze frei sind, und bildet keine
  seitengroßen Lücken; ein einzelnes länger lebendes Objekt hält eine Gruppe. db-01 wuchs so ohne Last um 1,4 MiB je
  Minute bei konstanten 0,73 MiB lebendigen Daten, in 128-KiB-Abbildungen, die bleiben.

## Optionen

1. **musl behalten, weniger allokieren** – Puffer wiederverwenden in Collectors und Log-Pipeline. Ohne neue Abhängigkeit,
   aber viele Stellen, und das Wachstum wird nur langsamer: mallocng gibt weiterhin nicht zuverlässig zurück.
2. **mimalloc** – schnell, aber gemessen 19 MiB eigener Speicher direkt nach dem Start (reserviert und hält Segmente);
   zähmen nur über `mi_option_set` (unsafe FFI) oder Umgebungsvariablen auf dem Host.
3. **jemalloc** (`tikv-jemallocator`) – gibt freie Seiten zuverlässig per `madvise` zurück. Mit den Vorgaben 19 MiB
   (eine Arena je Kern, Thread-Caches), mit einer Arena, ohne Thread-Cache und 1 s Abklingzeit 7,9 MiB eigener Speicher
   (RSS 13,4 MiB), flach über 10 min Log-Last mit Live-Ansichten; musl daneben 8,0 → 11,6 MiB.
4. **glibc statt musl** – widerspricht D1 (eine Binary für alle Distros, Alpine eingeschlossen).

## Entscheidung

Gewählt: **Option 3, jemalloc für den Agent in den statischen musl-Builds**, eingestellt beim Bauen
(`JEMALLOC_SYS_WITH_MALLOC_CONF = "narenas:1,tcache:false,dirty_decay_ms:1000,muzzy_decay_ms:0"` in
`agent/.cargo/config.toml`): eine Arena und kein Thread-Cache, weil der Agent einen async-Thread und wenige blockierende
hat; freie Seiten nach einer Sekunde zurück an den Kernel. Kein `unsafe`, keine Umgebungsvariable auf dem Host. Tests und
Builds für andere Ziele (macOS, glibc) behalten den Standard-Allocator (`cfg(all(target_os = "linux", target_env =
"musl"))`). Der root-Helper behält mallocng: Er läuft kurz und zählt seine Zeilen (ADR 0035).

## Konsequenzen

- Positiv: fester Speicher statt eines stetig wachsenden; die Demo misst am Ende eines vollen Laufs erneut (M4-32).
- Negativ: rund 320 KiB größere Binary; C-Code im Build (jemalloc 5.3, vendored in `tikv-jemalloc-sys`, BSD-2-Clause,
  Crate MIT/Apache-2.0, `cargo deny` ohne Befund). jemallocs `configure` archiviert mit `$AR`: Cross von macOS legt das
  `ar` des Systems die ELF-Objekte nicht ab (eine leere Bibliothek, der Link scheitert). Daher `agent/tools/ar`
  (Linux-Ziele über `zig ar`, sonst das System), gesetzt in `agent/.cargo/config.toml`.
- Absicherung: `scripts/check-agent-allocator.sh` prüft in `just build-agent`, `just test-packages` und der Demo, dass
  die Binary jemalloc mit diesen Einstellungen trägt – sonst fiele der Agent unbemerkt auf mallocng zurück.
- Security: eine weitere C-Bibliothek im Prozess des Agents (als User `sermon`, ohne root); jemalloc ist weit verbreitet
  und wird mit Renovate gepinnt gehalten (`=0.6.1`).
- Folgearbeiten: Die Allokationsrate selbst (Option 1) bleibt eine Verbesserung für die CPU, ist aber kein Muss mehr.
