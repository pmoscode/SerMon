# 0041 – Nodes deaktivieren und löschen

- Status: accepted
- Datum: 2026-11-26
- Bezug: KONZEPT.md §6 („Widerruf und Lebenszyklus“), §7, §2 (D29, D31); ADR 0010, ADR 0029, ADR 0031;
  IMPLEMENTIERUNGSPLAN M5-29

## Kontext

KONZEPT §6 nennt „Node löschen oder deaktivieren → Zertifikats-Serial auf die Denylist“, ließ aber offen, wer das
darf, was mit den Daten geschieht und wie ein Fehlgriff rückgängig wird. Organisationen werden seit M2-33 erst gesperrt
und nach einer Frist gelöscht (D29, ADR 0029); M3-10 hat das Löschen der Loki-Zeilen eines Nodes im Client vorbereitet.
Ein Node trägt personenbezogene Daten (Logins, Logs, Hostnamen), die nach dem Löschen nicht bleiben dürfen.

## Optionen

1. **Sofort löschen** – einfach, aber ein Fehlklick vernichtet die Historie; Loki löscht ohnehin erst nach der
   Wartefrist des Compactors.
2. **Deaktivieren, Löschen mit Frist** wie bei Organisationen – ein zweiter Schritt, rückgängig bis zum Ablauf.
3. **Nur ausblenden** – die Daten blieben, das widerspricht dem Zweck des Löschens.

## Entscheidung

Gewählt: Option 2.

- **Rechte:** neue Action `manage_node_lifecycle`, **Admin** auf der Gruppe des Nodes (wie Tags), jeweils mit
  **Step-up**; Löschen zusätzlich mit dem **Hostnamen als Bestätigung** (exakt, Leerzeichen am Rand zählen nicht).
  Fremde oder unbekannte Nodes sind `NOT_FOUND`.
- **Deaktivieren** (`DeactivateNode`): `deactivated_at` gesetzt, **alle Zertifikate des Nodes widerrufen** (die
  Denylist ist `revoked_at` in `agent_certificates`, geprüft bei jedem Verbindungsaufbau und jede Minute in offenen
  Sessions) und die Session auf dieser Instanz sofort beendet; `agent_certificate_usable` lehnt deaktivierte Nodes
  zusätzlich ab. Die Daten bleiben, der Alert-Engine ist der Node unbekannt (keine Welle von „Node offline“, offene
  Alerts enden), die Fleet zeigt ihn als „deaktiviert“.
- **Reaktivieren** heißt neu enrollen: Die Zertifikate sind widerrufen, also öffnet ein Operator „Übernahme erlauben“
  (M2-23), und das nächste Enrollment mit der `machine-id` übernimmt den Node mit ID und Historie und hebt die
  Deaktivierung auf. Ein eigenes „Reaktivieren“ ohne neues Zertifikat gibt es nicht.
- **Löschen** (`DeleteNode`): deaktiviert und setzt `deletion_due_at` auf jetzt plus Frist. Die Frist ist **7 Tage**,
  einstellbar über `SERMON_NODE_DELETION_GRACE` mit einer Untergrenze von **24 Stunden**. Bis dahin hebt
  `CancelNodeDeletion` das Löschen auf; der Node bleibt deaktiviert. Eine Übernahme ist während der Frist gesperrt.
- **Löschjob** stündlich in jeder Instanz (wie ADR 0029): zuerst die Loki-Löschung `{node_id="…"}` im Tenant der Org
  eintragen, dann `node_purge` (Definer, eine Transaktion, Zeile zuerst gesperrt): Metriken und Continuous Aggregates
  und Anomalie-Punkte der Serien des Nodes, Security-Events, Alert-Historie, zuletzt der Node mit allem, was per
  Fremdschlüssel an ihm hängt (Zertifikate, Serien, Alerts, Update-Stand, Kommandos, Login-Quellen). Lehnt Loki ab,
  bleibt der Node fällig und der nächste Lauf versucht es erneut. Der Eintrag `node.purge` landet im Audit-Log der Org
  in derselben Transaktion.
- **Audit:** `node.deactivate`, `node.schedule_deletion`, `node.cancel_deletion`, `node.purge` mit Gruppe und
  Hostname.

## Konsequenzen

- Positiv: Ein Fehlgriff ist bis zum Ablauf der Frist rückgängig; gelöschte Nodes hinterlassen in Datenbank und Loki
  keine Messwerte, Events, Alerts oder Logs.
- Negativ: Ein Node in der Frist zählt weiter zum Kontingent `max_nodes` und belegt seine `machine-id`; wer den Host
  sofort neu aufnehmen will, bricht das Löschen ab und erlaubt die Übernahme.
- Negativ: Das **Audit-Log** behält die Einträge mit ID und Hostname des Nodes (Hash-Kette, unveränderlich), ebenso
  das **Protokoll der Benachrichtigungen** bis zu dessen eigener Aufbewahrung und **Silences**, die den Node nennen
  (sie greifen ins Leere und laufen ab).
- Negativ: Loki führt die Löschung nach seiner Wartefrist aus (10 min, ADR 0029 Nachtrag M3-10); Abfragen sehen sie
  nach bis zu 5 Minuten. Bis dahin sind die Zeilen über SerMon nicht mehr erreichbar, weil der Node fehlt.
- Security: Ein gestohlener Schlüssel eines deaktivierten Nodes ist wertlos (widerrufen und Node abgelehnt). Ein
  Admin einer Gruppe kann nur Nodes seiner Gruppen löschen; der Löschjob löscht nur, was deaktiviert und fällig ist,
  und prüft das unter der Zeilensperre.
