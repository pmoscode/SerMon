# 0016 – Zielsysteme und Agent-Rechte: Distributionen, Einzeiler, Privilegientrennung, Live-Ansicht nur lesend

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D7, D8, D9, D19), §4.1, §4.2, §6, §9, §18.4; ADR 0002, 0010, 0012

## Kontext

Der Agent läuft auf fremden Servern mit Zugang zu Systemdaten. Jedes Recht, das er hat, steht einem Angreifer zur
Verfügung, der ihn oder den Server übernimmt. Gleichzeitig soll die Installation ohne Vorarbeit funktionieren und
später Paket-Updates und Reboots auslösen können, was root braucht.

## Optionen

1. **Agent als root:** einfach, aber jede Lücke im Agent oder ein kompromittierter Server ergibt root auf allen Nodes.
2. **Agent unprivilegiert, keine Updates:** sicher, aber ohne Update-Funktion.
3. **Agent unprivilegiert plus minimaler root-Helper** mit fester Liste von Operationen und lokalem Veto.
4. Installation nur über **Pakete**, nur über **Skript**, oder **Skript mit Token, Pakete optional**.
5. Live-Ansicht mit **Eingriffen** (Prozesse beenden) oder **nur lesend**.

## Entscheidung

- **D7 – Zielsysteme:** Debian/Ubuntu, RHEL/Fedora/Rocky/Alma, Alpine; amd64 und arm64. Eine statische musl-Binary pro
  Architektur (ADR 0002); systemd-Unit und OpenRC-Skript.
- **D8 – Einzeiler mit Enrollment-Token** (`curl … | sh`) mit Signaturprüfung im Skript und „Skript erst ansehen“;
  Pakete als gleichwertige Alternative später (ADR 0012, O5).
- **D9 – Privilegien getrennt:** Der Agent läuft als User `sermon` in einer gehärteten Unit (Score < 2,0). Ein eigener
  `sermon-helper` (< 1 000 LoC) läuft als root, nimmt über einen Unix-Socket mit `SO_PEERCRED`-Prüfung nur ein festes
  Enum an Operationen an (Updates auflisten/anwenden, Reboot, Self-Update) und hat lokale Verbote, die der Server
  nicht überschreiben kann.
- **D19 – Live-Ansicht nur lesend:** Prozesslisten werden angezeigt, aber keine Signale oder Kommandos gesendet.

## Konsequenzen

- Positiv: Ein kompromittierter Agent oder Server erreicht auf den Nodes nur Lesezugriff und die festen
  Helper-Operationen, und die nur, wenn lokal erlaubt.
- Negativ: Ohne `CAP_SYS_PTRACE` fehlen IO- und FD-Details fremder Prozesse (O3); Log-Zugriff braucht die Gruppen
  `systemd-journal`/`adm`.
- Security: Jede neue Helper-Operation braucht ein eigenes ADR; keine generischen Kommandos im Protokoll.
- Folgearbeiten: Helper mit Funktionen ab M4 (Updates); Paket-Repo vor v1.0.
