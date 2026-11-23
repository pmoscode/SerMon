# „n/a“ in der Live-Ansicht

Der Agent konnte den Wert nicht lesen. Er läuft als User `sermon` ohne besondere Rechte und sieht von fremden Prozessen nur, was jeder User sehen darf.

## Warum

- Linux schützt Teile von `/proc/<pid>` fremder Prozesse, je nach Einstellung (`hidepid`) auch Speicher, Laufzeit oder Befehlszeile.
- Ein Prozess, der während des Lesens endet, kann unvollständig ankommen.
- Offene Dateien und E/A fremder Prozesse liest SerMon bewusst nicht: Dafür bräuchte der Agent `CAP_SYS_PTRACE`, und das bekommt er nicht.

## Was tun

Meist nichts – die übrigen Werte stimmen. Wer mehr braucht, schaut direkt auf dem Host mit `sudo` nach.
