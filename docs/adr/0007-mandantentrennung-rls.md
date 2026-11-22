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
- LOGIN und Passwort setzt der Betrieb: `deploy/compose/initdb/010-roles.sh` legt mit eigenen Zufalls-Secrets `sermon_app`, `sermon_definer` und die Owner-Rolle `sermon_owner` (ohne Superuser) an, in der Entwicklungsumgebung wie in Produktion (M1-05, M2-36; ADR 0028).
- **`postgres.Open` verweigert den Start**, wenn die verbundene Rolle Superuser ist, `BYPASSRLS` hat oder Tabellen im Schema besitzt (`ErrPrivilegedRole`). Eine Fehlkonfiguration wird so zum Startfehler statt zum stillen Datenleck.

### Tenant-Kontext

- `tenant.Scope` (Domäne) enthält genau eine Organisation sowie die erlaubten Node-Gruppen bzw. „alle“. Die Nullwerte sind ungültig und gewähren nichts.
- `postgres.DB.InTenant(ctx, scope, fn)` öffnet eine Transaktion und setzt `set_config('sermon.org_id', <org>, true)`, also transaktionslokal. Danach erhält `fn` die typisierten sqlc-Queries auf dieser Transaktion.
  - Ein ungültiger Scope ergibt `ErrInvalidScope`; `fn` wird dann nicht aufgerufen.
  - Gibt `fn` einen Fehler zurück, wird zurückgerollt.
- In SQL liefert `current_org_id()` die Organisation der Transaktion bzw. `NULL`. **Ohne gesetzten Tenant sind alle Zeilen unsichtbar.**

### Policies

- Alle Tenant-Tabellen haben `ENABLE` und **`FORCE ROW LEVEL SECURITY`**.
- `alert_history` ist für `sermon_app` nur anhängbar. Löschen darf die App nur Einträge, die älter als ein Jahr sind:
  Eine einschränkende Policy `delete_only_expired` (`AS RESTRICTIVE FOR DELETE`) wird mit der Tenant-Policy
  UND-verknüpft (Aufbewahrung, M2-16).
- `organizations` und `memberships`: `USING` und `WITH CHECK` auf `= current_org_id()`. Lesen und Schreiben fremder Organisationen ist damit unmöglich.
- `users` sind globale Identitäten, weil ein User Mitglied mehrerer Organisationen sein kann:
  - Lesen und Ändern nur über eine Mitgliedschaft in der aktuellen Organisation.
  - Anlegen ist erlaubt (Einladung).
  - **Folge:** `INSERT … RETURNING` auf `users` scheitert, weil die neue Zeile ohne Mitgliedschaft unsichtbar ist. `CreateUser` gibt deshalb nichts zurück; die Mitgliedschaft wird in derselben Transaktion angelegt.

### Zugriffe ohne Mandant (Ergänzung M1-07, überarbeitet mit M1-02)

Manche Prüfungen laufen, bevor ein Mandant bekannt ist. Beispiele sind die Sperrprüfung von Agent-Zertifikaten beim
TLS-Handshake und die Suche eines Users per E-Mail beim Login. Dafür gilt:

- **`SECURITY DEFINER`-Funktionen** mit festem `search_path`, die genau eine Frage minimal beantworten:
  - `agent_certificate_usable(serial, at)`: ja/nein. Seit M1-11 gilt eine Sperre mit Zeitpunkt in der Zukunft
    (Übergangszeit nach einer Rotation) als noch gültig, und Zertifikate eines gesperrten Nodes sind unbrauchbar.
  - `auth_find_user(email)`: nur die User-ID
  - `auth_user_organizations(user)`: nur ID und Name der Orgs
  - `auth_user_profile(user)`: nur E-Mail und Anzeigename (Session-Ansicht, M1-02)
  - `invitation_by_token(hash, at)`: eine gültige Einladung zum Token-Hash (M1-04, ADR 0008)
  - `enrollment_token_by_hash(hash, at)`: ein nutzbares Enrollment-Token zum Hash (M1-08)
  - `admin_organizations()` (vorher `admin_list_organizations()`): Name, Mitgliederzahl und Kontingente aller Orgs für
    den Superadmin, keine Mandantendaten (M1-04, M2-32)
  - `admin_storage_totals()`: Gesamtgröße der Metrik-Hypertables samt Continuous Aggregates und der Event-Partitionen,
    nur Größen (M2-32); die Anteile pro Org zählt der Server in Tenant-Transaktionen
  - `org_purge(org, at)`: löscht eine gesperrte, fällige Org samt Metriken, Events, Audit-Log und Konten ohne weitere
    Mitgliedschaft (M2-33, ADR 0029); setzt selbst `sermon.org_id`, die Mandanten-Policies begrenzen das Löschen auf
    diese Org. Dafür hat `sermon_definer` DELETE (und das dafür nötige SELECT) auf `organizations`, `nodes`,
    `audit_log`, `users`, `metrics_raw` und die Materialisierung der Continuous Aggregates; die Policies
    `definer_purge` auf `audit_log` (`current_org_id()`) und `users` (nur DELETE, die IDs bestimmt die Funktion). Die
    Kaskaden der Fremdschlüssel laufen als Eigentümer der Tabellen.
  - Seit M2-33 lehnen `agent_certificate_usable`, `enrollment_token_by_hash`, `invitation_by_token` und
    `auth_user_organizations` eine gesperrte Org ab (`locked_at`); `admin_organizations()` liefert zusätzlich `locked`.
  - `pki_certificates_in_use(authority, at)`: nur die Anzahl gültiger Zertifikate einer CA (M1-11b)
  - `metric_points_raw/_1m/_1h(series_ids, from, to, step)`: Messpunkte der angegebenen Serien (M1-22, ADR 0001).
    Hier gilt die RLS bewusst **auch für `sermon_definer`**: Auf `series` gibt es keine `definer_read`-Policy, die
    Funktionen liefern also nur Punkte von Serien des aktuellen Mandanten, selbst wenn fremde Serien-IDs übergeben
    werden. `sermon_app` darf die Metrik-Tabellen nur beschreiben (INSERT), nicht lesen, weil TimescaleDB auf
    komprimierten Hypertables und Continuous Aggregates keine RLS kann. Gegenprobe: Mit einer `definer_read`-Policy
    auf `series` sieht eine fremde Org die Punkte.

  Suchen per Token gehen nur über den Hash; eine Liste lässt sich damit nicht abfragen. Die Berechtigung
  (z. B. Superadmin) prüft die App-Schicht vor dem Aufruf.

  `PUBLIC` hat kein EXECUTE-Recht, nur `sermon_app`. Unbekannte Werte ergeben „nein“ bzw. nichts (fail closed).
- **Eigentümerin dieser Funktionen ist die Rolle `sermon_definer`:**
  - kein Login, kein BYPASSRLS, kein CREATE im Schema (nur kurz während der Migration),
  - SELECT nur auf die benötigten Tabellen und eine lesende Policy `definer_read` nur für sich,
  - `sermon_app` ist kein Mitglied.

  So bleibt `FORCE ROW LEVEL SECURITY` auf **allen** Mandanten-Tabellen aktiv, auch auf `agent_certificates`. Die
  anfängliche Ausnahme ohne `FORCE` aus M1-07 ist entfallen. Sie hätte mit einem Eigentümer ohne Superuser-Rechte
  ohnehin nicht funktioniert, weil `FORCE` dann auch für ihn gilt.
- **Bereitstellung:** Die Migrationsrolle braucht Mitgliedschaft in `sermon_definer`. Hat sie die Rolle selbst
  angelegt (CREATEROLE), weist sie sie sich selbst zu. Existiert die Rolle schon, z. B. bei einem gemanagten
  PostgreSQL, ist einmalig `GRANT sermon_definer TO <owner>` nötig. Die Migration bricht sonst mit genau diesem
  Hinweis ab.
- Instanzweite Tabellen ohne Mandantenbezug haben keine RLS:
  - `pki_authorities`: private Schlüssel sind versiegelt.
  - `user_credentials`: Passwörter liegen nur als Argon2id-Hash vor.
  - `sessions`: werden über den Hash des Tokens gefunden. Die Tabelle hat zwar `org_id`, ist aber als Ausnahme im
    RLS-Test eingetragen.
  - `instance_setup`: nur der Hash des Setup-Tokens (M1-05).
  - `superadmins`: nur User-IDs; `sermon_app` darf lesen und einfügen (M1-05).

  Ein Test prüft, dass **jede** Tabelle mit `org_id` RLS mit ENABLE und FORCE hat. Ausnahmen müssen dort mit
  Begründung stehen.
- **Einmalige Ausnahme in Migration 00012:** Für das Nachtragen von `agent_certificates.authority_id` über alle Orgs
  wird `FORCE` nur für dieses eine `UPDATE` aufgehoben und direkt danach wieder gesetzt. Der Eigentümer umgeht RLS
  ohne `FORCE`; mit `FORCE` würde das `UPDATE` bei einem Eigentümer ohne Superuser-Rechte still nichts ändern.
- **Getestet mit einem Eigentümer ohne Superuser-Rechte (Produktionsfall gemanagter Datenbanken):**
  - Migration, Login-Lookups und Sperrprüfung funktionieren.
  - `FORCE` hindert auch den Eigentümer, ohne Mandant zu schreiben.
  - Die Migration meldet die fehlende Rollenzuweisung verständlich.
  - Gegenprobe: Mit `SECURITY INVOKER` schlagen die Tests fehl.

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

## Nachtrag M1-35 bis M1-40 (Security-Review)

- `sermon_app` darf `users` nicht mehr ändern (Migration 00016): Konten sind instanzweit, ein Org-Admin könnte sonst
  die E-Mail-Adresse (den Login) eines Users ändern, der auch in anderen Orgs ist.
- Instanzweite Schreibzugriffe mit Rechte-Folgen laufen nur über `SECURITY DEFINER`-Funktionen von `sermon_definer`:
  die Widerrufsprüfung mit Org und Node (00018) sowie das Ausgeben und Verbrauchen des Setup-Tokens (00019). Den
  Superadmin legt nur der Verbrauch des Tokens an; `sermon_app` hat auf `superadmins` und `instance_setup` nur noch
  `SELECT`.


## Nachtrag M2-07: partitionierte Tabelle `security_events`

- **Kein DDL zur Laufzeit:** Security-Events liegen in einer nach Monat partitionierten Tabelle. Neue Partitionen zur
  Laufzeit hätten `CREATE` für `sermon_definer` gebraucht. Das widerspricht der Regel oben (`CREATE` nur während der
  Migration), und `TestDefinerRole_HasNoCreatePrivilegeAndNoLogin` hat den ersten Entwurf genau daran scheitern lassen.
- **Partitionen vorab:** Die Migration legt deshalb die Monate von 2026-10 bis 2036-12 im Voraus an.
  - Eigentümer von Elterntabelle und Partitionen ist `sermon_definer`; das `CREATE` dafür gilt nur während der
    Migration.
  - Die Definer-Funktion `security_events_expire` **löscht** ganze Monate für die Retention. `DROP` braucht
    Eigentümerschaft, kein `CREATE`. Seit M2-28 (Migration 00037) löscht sie nie etwas der letzten 365 Tage, nach
    der Uhr der Datenbank: Ein Sprung der Server-Uhr hätte sonst die Events aller Orgs gelöscht.
  - `security_events_horizon` meldet das Ende der letzten Partition. Die Wartung des Servers warnt ein Jahr vorher;
    spätere Migrationen verlängern.
- **RLS:** Die Elterntabelle hat RLS mit der üblichen Policy, und `sermon_app` darf dort nur `SELECT` und `INSERT`. Jede
  Partition hat ebenfalls `ENABLE` und `FORCE ROW LEVEL SECURITY`, aber keine Rechte für `sermon_app`. Direkter Zugriff
  auf eine Partition, DDL, `UPDATE` und `DELETE` sind verweigert; die Integrationstests prüfen das.
  `TestRLS_EveryTenantTableIsForced` erfasst die Partitionen automatisch.
- **Verworfen, Hypertable von TimescaleDB:** Ausprobiert mit dem gepinnten Image 2.32.0-pg17. RLS auf der Hypertable
  wirkt nur beim Zugriff über sie. Die Chunks (`_timescaledb_internal`) haben **keine RLS**, erben aber die Rechte der
  Hypertable. Eine Rolle ohne Sonderrechte las ohne Mandant über den Chunk die Zeilen **aller** Orgs.

## Nachtrag M2-32: Migration mit `NO FORCE`

- Migration 00035 benennt doppelte Org-Namen um, bevor ein eindeutiger Index auf `lower(name)` entsteht. Die Rolle der
  Migration sieht wegen `FORCE ROW LEVEL SECURITY` keine Zeilen von `organizations`; die Migration schaltet `FORCE` dafür
  aus und in derselben Transaktion wieder ein. `TestRLS_EveryTenantTableIsForced` prüft danach weiter, dass `FORCE` gilt.
