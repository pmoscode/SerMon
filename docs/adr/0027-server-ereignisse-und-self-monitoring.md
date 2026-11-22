# 0027 – Ereignisse des Servers als Security-Events, Self-Monitoring für Superadmins

- Status: accepted
- Datum: 2026-11-22
- Bezug: KONZEPT.md §6, §11, §12, §23; ADR 0011, ADR 0019; Threat Model T3, T4, T37; IMPLEMENTIERUNGSPLAN M2-24

## Kontext

Drei sicherheitsrelevante Vorgänge geschehen im Server, nicht auf einem Node: ein Enrollment-Token wird benutzt (T3),
ein Node wird vorsorglich gesperrt (zweite Session oder ersetztes Zertifikat, T4), ein Agent rotiert seinen Schlüssel
von einer neuen Adresse. Bisher standen sie nur im Audit-Log, das niemanden benachrichtigt.

Daneben soll der Server sich selbst überwachen (T37): Kommen Agent-Daten verspätet an, verwirft der Ingest Samples,
läuft die Datenbank voll? Das betrifft keine Organisation, sondern die Instanz. Kanäle und Alerts gehören aber zu
Organisationen, und es gibt keine Oberfläche nur für Superadmins. Der Server kennt die Größe der Datenbank, nicht den
freien Platz auf dem Datenträger.

## Entscheidung

- **Server-Ereignisse sind Security-Events** mit den Arten `enrollment.token_used` (Node: der neue bzw. übernommene,
  Felder `token`, `ip` = Absender des Enrollments), `node.locked` (`reason`) und `node.rotation_new_ip` (`ip`,
  `previous_ip`). Der Server schreibt sie in **derselben Transaktion** wie das Enrollment, die Sperre bzw. die Notiz:
  genau ein Event pro Vorgang, nie eines ohne den Vorgang. Ein Agent kann diese Arten nicht senden (sein Protokoll hat
  sie nicht). Standardregeln: `security.enrollment.token_used` (Warnung), `security.node.locked` (kritisch),
  `security.node.rotation_new_ip` (Warnung). Damit gelten dieselben Kanäle, Policies, Silences, Rechte und die
  Retention wie für alle Security-Events.
- **„Agent hat Daten verworfen“** braucht keinen neuen Regeltyp: `threshold` bekommt die Aggregation `increase` (Zuwachs
  eines kumulativen Zählers im Fenster; fällt er, zählt er ab 0 weiter). Regel `system.agent.data_dropped`.
- **Self-Monitoring** (`selfmonitor`) prüft jede Minute: Ingest-Lag (Warnung ab 60 s, kritisch ab 10 min), verworfene
  Samples (Zuwachs innerhalb von 15 min: implausible Zeiten und Rate-Limit, zu späte Fenster, unbekannte Metriken,
  Serien-Limit) und die Größe der Datenbank gegen `SERMON_DATABASE_SIZE_LIMIT` (Warnung ab 80 %, kritisch ab 90 %; ohne
  Limit keine Prüfung). Bei jedem Wechsel der Stufe bekommen die Superadmins eine Mail über das SMTP der Instanz (auch
  die Entwarnung); die Adressen liefert die Definer-Funktion `superadmin_emails()`, sonst nichts aus `users`. In der UI
  sehen Superadmins die offenen Probleme als Band unter der Kopfzeile (`Session.instance_problems`); Mitglieder von
  Organisationen sehen nichts davon.

## Konsequenzen

- **Positiv:** Ein gestohlenes Token, ein kopierter Schlüssel oder ein überlasteter Server fallen auf, ohne dass
  jemand Audit-Logs oder `/metrics` liest.
- **Negativ:**
  - Jedes geplante Enrollment erzeugt einen Alert (Warnung, 15 min). Wer viele Nodes ausrollt, schaltet die Regel ab
    oder legt eine Silence an.
  - Ohne SMTP bekommen Superadmins keine Mail, nur den Hinweis in der UI.
  - Der Füllstand ist relativ zu einem Limit, das der Betreiber setzt; den freien Platz der Platte kennt der Server nicht.
  - Der Zustand des Self-Monitorings lebt im Speicher jeder Server-Instanz: Mehrere Instanzen mailen jede für sich.
- **Security:** Die Definer-Funktion gibt nur die Adressen der Superadmins heraus. Die Events entstehen nur im Server.

## Nachtrag M2-37

- Neuer Check **`dropped_security_events`** (Warnung): Zuwachs der verworfenen *seltenen* Security-Events innerhalb von
  15 min (erfolgreiche Logins, sudo, Konten, System; ADR 0023, Nachtrag M2-37). Verworfene Fehlversuche zählen nicht:
  Bei einem Angriff sind sie erwartet, und der Brute-Force-Alert meldet ihn schon.
