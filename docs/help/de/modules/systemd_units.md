# systemd-Units

Zustand und automatische Neustarts ausgewählter systemd-Units – ergänzend zu den fehlgeschlagenen Units, die der Agent
ohnehin meldet (`sermon_systemd_unit_failed`). Das Modul ist ab Werk aus.

## Voraussetzungen

Ein Host mit systemd. Der Agent fragt `systemctl show` mit festen Eigenschaften und geprüften Namen, ohne Shell und
ohne Zusatzrechte (systemd antwortet über D-Bus auch einem User ohne Rechte). Auf Alpine (OpenRC) meldet das Modul
„this host runs no systemd“.

## Einstellungen

- **Units:** bis zu 32 Namen mit Typ, etwa `nginx.service`, `postgresql@17-main.service`, `backup.timer`.

## Metriken

- `sermon_systemd_units_loaded`: systemd kennt die Unit.
- `sermon_systemd_units_active`, `sermon_systemd_units_failed`: aktiv bzw. fehlgeschlagen.
- `sermon_systemd_units_restarts`: automatische Neustarts (`Restart=`); steigt der Wert stetig, stürzt der Dienst immer
  wieder ab, auch wenn er zwischendurch aktiv aussieht.

## Wenn es nicht läuft

- `sermon_systemd_units_loaded` 0: Name falsch oder Dienst nicht installiert.
- Ein Alias (etwa `dbus.service` auf Rocky Linux) zählt unter dem konfigurierten Namen.
