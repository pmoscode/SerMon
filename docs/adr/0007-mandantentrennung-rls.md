# 0007 – Mandantentrennung in PostgreSQL: Tenant-Scope, RLS und getrennte Rollen

- Status: accepted
- Datum: 2026-11-19
- Bezug: KONZEPT.md §4.3, §7, §18.1 (Information Disclosure), ADR 0001; IMPLEMENTIERUNGSPLAN M0-08

## Kontext

SerMon ist mandantenfähig (D5). Ein User einer Organisation darf **niemals** Daten einer anderen sehen.
Das Konzept verlangt zwei Verteidigungslinien:

1. **Primär:** Jede Datenabfrage ist im Code an einen `tenant.Scope` gebunden.
2. **Zweitlinie:** PostgreSQL-Row-Level-Security (RLS) erzwingt die Organisation zusätzlich in der Datenbank, falls im Code eine Abfrage den Scope vergisst.

Dabei gibt es eine Falle: **RLS gilt nicht für Superuser und Rollen mit `BYPASSRLS`.**
Für Tabelleneigentümer gilt es nur mit `FORCE`, und der Eigentümer kann RLS jederzeit abschalten.
In der Standardkonfiguration des Postgres-Images ist der einzige Login-User Superuser. RLS wäre damit wirkungslos, ohne dass es auffällt.

## Entscheidung

### Rollen

| Rolle | Rechte | Verwendung |
|---|---|---|
| Owner (z. B. `sermon`) | Eigentümer aller Objekte | **nur** Migrationen (`postgres.Migrate`) |
| `sermon_app` | `NOSUPERUSER NOBYPASSRLS`, besitzt keine Tabellen, nur DML-Rechte | Laufzeit von sermon-server (`postgres.Open`) |

- Die Migration legt `sermon_app` ohne LOGIN an, falls die Rolle fehlt.
- LOGIN und Passwort setzt der Betrieb: In der Entwicklungsumgebung erledigt das `deploy/dev/initdb/010-app-role.sh` mit einem eigenen Zufalls-Secret; in Produktion `sermon-server init` (M1-05).
- **`postgres.Open` verweigert den Start**, wenn die verbundene Rolle Superuser ist, `BYPASSRLS` hat oder Tabellen im Schema besitzt (`ErrPrivilegedRole`). Eine Fehlkonfiguration wird so zum Startfehler statt zum stillen Datenleck.

### Tenant-Kontext

- `tenant.Scope` (Domäne) enthält genau eine Organisation sowie die erlaubten Node-Gruppen bzw. „alle“. Die Nullwerte sind ungültig und gewähren nichts.
- `postgres.DB.InTenant(ctx, scope, fn)` öffnet eine Transaktion und setzt `set_config('sermon.org_id', <org>, true)`, also transaktionslokal. Danach erhält `fn` die typisierten sqlc-Queries auf dieser Transaktion.
  - Ein ungültiger Scope ergibt `ErrInvalidScope`; `fn` wird dann nicht aufgerufen.
  - Gibt `fn` einen Fehler zurück, wird zurückgerollt.
- In SQL liefert `current_org_id()` die Organisation der Transaktion bzw. `NULL`. **Ohne gesetzten Tenant sind alle Zeilen unsichtbar.**

### Policies

- Alle Tenant-Tabellen haben `ENABLE` und **`FORCE ROW LEVEL SECURITY`**.
- `organizations` und `memberships`: `USING` und `WITH CHECK` auf `= current_org_id()`. Lesen und Schreiben fremder Organisationen ist damit unmöglich.
- `users` sind globale Identitäten, weil ein User Mitglied mehrerer Organisationen sein kann:
  - Lesen und Ändern nur über eine Mitgliedschaft in der aktuellen Organisation.
  - Anlegen ist erlaubt (Einladung).
  - **Folge:** `INSERT … RETURNING` auf `users` scheitert, weil die neue Zeile ohne Mitgliedschaft unsichtbar ist. `CreateUser` gibt deshalb nichts zurück; die Mitgliedschaft wird in derselben Transaktion angelegt.

### Tests (Integration, Testcontainers mit demselben Image wie `deploy/dev/compose.yaml`)

- Ohne Tenant sind 0 Zeilen sichtbar (alle Tabellen).
- Ein Tenant sieht nur die eigene Organisation und die eigenen User. Er kann fremde Organisationen weder lesen noch anlegen.
- Der Tenant-Kontext überträgt sich nicht auf spätere Abfragen derselben Verbindung (Pool mit genau einer Verbindung).
- Ein ungültiger Scope wird abgelehnt; bei einem Fehler wird zurückgerollt.
- Die App-Rolle kann RLS weder abschalten (`ALTER TABLE`) noch umgehen (`row_security = off` führt zu einem Fehler).
- `Open` lehnt die Owner-Rolle ab. Die Migration ist idempotent und funktioniert auch mit vorab angelegter App-Rolle.
- **Gegenprobe:** Ohne RLS auf `organizations` schlagen 5 Tests fehl.

## Konsequenzen

- **Positiv:**
  - Eine fehlende `WHERE org_id = …`-Bedingung führt nicht zu einem Datenleck.
  - Eine Fehlkonfiguration der Rollen verhindert den Start.
  - Die Isolation ist an der Datenbank selbst getestet.
- **Negativ / Risiken:**
  - Flows ohne Tenant (Login per E-Mail, Superadmin-Verwaltung der Orgs) brauchen einen eigenen, eng begrenzten Weg, z. B. `SECURITY DEFINER`-Funktionen mit minimalem Ergebnis. Das wird mit M1-02 und M1-04 entschieden.
  - Die Policies kosten etwas Performance, vor allem die `EXISTS`-Abfrage bei `users`; Indizes sind vorhanden.
  - Für die Metrik-Hypertables (ADR 0001) ist RLS noch zu prüfen (Spike M0-09). Die primäre Isolation über den Scope gilt dort ohnehin.
- **Security-Auswirkungen:**
  - Zwei unabhängige Verteidigungslinien für die Mandantentrennung.
  - Getrennte Zugangsdaten für Migration und Laufzeit; das Owner-Passwort braucht der laufende Server nicht.
