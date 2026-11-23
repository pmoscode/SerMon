# 0026 – Zustands-Collectors: adjtimex, systemctl und erholte Units

- Status: accepted
- Datum: 2026-11-22
- Bezug: KONZEPT.md §8.1, §12.3, §18.4; ADR 0012, ADR 0016; IMPLEMENTIERUNGSPLAN M2-22

## Kontext

M2-22 bringt drei Zustände: RAID (`/proc/mdstat`), Zeitsynchronisation und fehlgeschlagene systemd-Units. RAID ist eine
Datei wie andere Collector-Quellen. Die beiden anderen brauchen mehr:

- **Zeitsynchronisation:** Ob die Uhr synchronisiert ist, wie groß der Offset und der geschätzte Fehler sind, weiß
  portabel nur der Kernel (`adjtimex`), egal ob chrony, ntpd oder systemd-timesyncd sie steuert. rustix bietet den
  Syscall nicht. Die gehärtete Unit verbietet ihn auch zum Lesen: `ProtectClock=yes` und `~@privileged` (enthält
  `@clock`) liefern EPERM; `@system-service` enthält ihn nicht. Versucht in einem Debian-13-Container mit systemd 257.
- **Fehlgeschlagene Units:** systemd gibt sie über D-Bus heraus. Als normaler User spricht `systemctl` über den
  System-Bus mit systemd (root nutzt `/run/systemd/private`). Ein Alert soll die Unit nennen, braucht also eine Serie
  pro Unit. Fehlende Daten beenden einen Alert nicht (`NoData` lässt die Instanz in ihrem Zustand): Fiele die Serie
  einer erholten Unit einfach weg, feuerte der Alert ewig.

## Entscheidung

- **`adjtimex` mit einem gekapselten `unsafe`:** ein Aufruf mit `modes = 0` (nur lesen) über `libc`, mit
  `#[allow(unsafe_code)]` und `SAFETY`-Kommentar, nur unter Linux; dahinter ein Port (`ClockSource`) mit Fake für die
  Tests. Die Unit verliert `ProtectClock=yes` und erlaubt nach den Verboten ausdrücklich
  `SystemCallFilter=adjtimex clock_adjtime`. Stellen kann der Agent die Uhr nicht: Dafür braucht es `CAP_SYS_TIME`, die
  Unit gibt keine Capabilities (`CapabilityBoundingSet=` leer, `NoNewPrivileges`). `systemd-analyze security`: 1,9
  statt 1,6, die Grenze bleibt 2,0.
- **`systemctl list-units --state=failed --plain --no-legend --no-pager --full`:** feste Argumente, leere Umgebung
  (nur `LC_ALL=C`, `SYSTEMD_COLORS=0`), kein Shell, 10 s Zeitlimit (danach `kill`), höchstens 256 KiB Ausgabe, alle
  30 s; nur wenn `/run/systemd/system` existiert. Unit-Namen werden geprüft (bekannte Endung, nur `[A-Za-z0-9:_.@\-\\]`,
  ≤ 255 Zeichen), höchstens 50. Ohne D-Bus fehlt die Metrik; der Agent sagt es einmal (debug).
- **Erholte Units:** Der Agent merkt sich jede fehlgeschlagene Unit mit Zeitpunkt und meldet sie nach der Erholung noch
  24 h lang mit 0, sodass der Alert endet. Das Gedächtnis liegt im Zustandsverzeichnis (`systemd-failed`) und übersteht
  Neustarts; `sermon-agent uninstall` und die Pakete löschen es mit. Höchstens 50 Units, die ältesten fallen heraus.
- **Regeln:** `system.raid.degraded` (kritisch, 1 min), `system.time.unsynchronized` (Warnung nach 30 min, ohne
  „vorher gesehen“: auch ein Host, der nie synchron war, soll auffallen), `system.systemd.unit_failed` (Warnung nach
  1 min, eine Instanz pro Unit).

## Alternativen

- **Zeitsync über Dateien** (`/run/systemd/timesync/synchronized`): ohne `unsafe`, aber nur für timesyncd und ohne
  Offset.
- **Nur die Anzahl fehlgeschlagener Units:** einfacher, aber der Alert sagt nicht, welche Unit.
- **D-Bus direkt** (zbus): eine große Abhängigkeit für eine Liste, die `systemctl` liefert.

## Konsequenzen

- **Positiv:** Zeitsync für alle NTP-Dienste; Alerts nennen die Unit und enden, wenn sie sich erholt.
- **Negativ:**
  - Der erste `unsafe`-Code im Agent (zwei Zeilen). Der Fuzzer erreicht ihn nicht; er liest nur.
  - Der Agent startet einen Prozess alle 30 s (wenige Millisekunden).
  - Auf Hosts ohne D-Bus (minimale Container) gibt es keine Unit-Zustände.
  - Wird eine Unit gelöscht, während der Agent aus ist, meldet er nach dem Start 0 und der Alert endet; wird das
    Gedächtnis gelöscht, bleibt ein Alert stehen, bis die Unit wieder fehlschlägt und sich erholt.
- **Security:** Kein freies Kommando: Programm und Argumente sind fest, die Ausgabe wird nur als Liste von
  Unit-Namen gelesen. Threat Model T72.

## Nachtrag M2-39: Template-Units

- Instanzen einer Template-Unit zählen als das Template (`sshd@.service`, `getty@.service`): Socket-aktiviertes sshd
  benennt jede Verbindung nach ihren Adressen, sonst entstünde pro Besucher eine Serie. Welche Instanz ausfiel, zeigt
  `systemctl --failed` auf dem Host.
- Höchstens 100 neue Unit-Namen pro Tag (im Speicher des Agents, ein Neustart beginnt neu); darüber hinaus erst wieder
  am nächsten Tag.
