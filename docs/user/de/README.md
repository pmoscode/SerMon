# SerMon – Benutzerhandbuch

Dieses Handbuch führt durch die Arbeit mit SerMon: vom Überblick über die Flotte bis zu Alerts, Logs, Updates und
eigenen Dashboards. Es beschreibt die Abläufe; die Einzelheiten jeder Seite und jeder Kennzahl stehen in den
Hilfetexten, die SerMon auch selbst anzeigt (ⓘ neben einer Kennzahl, `?` oder das Hilfe-Symbol oben für die Seite).
Die Links unten führen zu genau diesen Texten.

Für Betreiber der Instanz gibt es die [Admin-Doku](../../admin/installation.md) und die
[Vorlage zur DSGVO-Dokumentation](../../privacy/de/README.md).

English version: [User guide](../en/README.md).

## Inhalt

1. [Anmelden und das eigene Konto](#1-anmelden-und-das-eigene-konto)
2. [Rollen und Rechte](#2-rollen-und-rechte)
3. [Flotte](#3-flotte)
4. [Die Node-Seite](#4-die-node-seite)
5. [Nodes hinzufügen, deaktivieren und löschen](#5-nodes-hinzufügen-deaktivieren-und-löschen)
6. [Alerts und Silences](#6-alerts-und-silences)
7. [Benachrichtigungen](#7-benachrichtigungen)
8. [Logs und Live-Ansicht](#8-logs-und-live-ansicht)
9. [Security-Dashboard](#9-security-dashboard)
10. [Updates und Update-Policies](#10-updates-und-update-policies)
11. [Module](#11-module)
12. [Dashboards](#12-dashboards)
13. [Kiosk-Anzeigen](#13-kiosk-anzeigen)
14. [Vergleich, Heatmap, Kapazität und Anomalien](#14-vergleich-heatmap-kapazität-und-anomalien)
15. [Die Organisation verwalten](#15-die-organisation-verwalten)

## 1. Anmelden und das eigene Konto

Du meldest dich mit E-Mail und Passwort an, mit einem Passkey oder – wenn die Instanz einen Identity Provider hat –
über SSO. Neue Mitglieder kommen über eine Einladung per E-Mail dazu; der Link gilt einmal innerhalb von 72 Stunden.

Im Kontomenü unter **Kontosicherheit** richtest du die Zwei-Faktor-Anmeldung ein (Authenticator-App und Recovery-Codes),
fügst Passkeys hinzu und verknüpfst dein Konto mit dem Identity Provider. Verlangt deine Organisation einen zweiten
Faktor, sind ihre Seiten erst danach erreichbar.

Sensible Aktionen – Rechte ändern, Updates starten, Kanäle ändern und viele mehr – fragen noch einmal nach Code,
Passkey oder Passwort. Die Bestätigung gilt 15 Minuten.

- [Sicherheit (Zwei-Faktor, Passkeys, SSO)](../../help/de/pages/security.md)
- [Erneute Bestätigung](../../help/de/concepts/step-up.md)

Die Sprache (Deutsch, Englisch) wechselst du im Kontomenü; bist du Mitglied mehrerer Organisationen, wählst du die
Organisation im Kopf der Seite.

## 2. Rollen und Rechte

Jedes Mitglied hat Rollen auf Node-Gruppen: **Betrachter** sehen, **Operatoren** handeln (quittieren, stummschalten,
Updates, Reboots, Nodes hinzufügen), **Admins** einer Gruppe verwalten deren Nodes, **Admins für alle Gruppen** die
ganze Organisation. Nodes einer Gruppe ohne Rolle darauf siehst du nicht – auch nicht in Suchen, Dashboards oder
Exporten. Fehlt dir ein Knopf, fehlt dir meist die Rolle; frag einen Admin deiner Organisation.

- [Rollen und Rechte](../../help/de/concepts/roles.md)
- [SSO-Gruppen](../../help/de/concepts/oidc-group-mappings.md)

## 3. Flotte

Die Startseite **Flotte** listet alle Nodes, die du sehen darfst, mit Status (online heißt: Kontakt in den letzten
60 Sekunden) und den wichtigsten Kennzahlen, als Kacheln oder Tabelle. Die Suche (Name und Tags) und die Filter nach
Gruppe und Tag grenzen die Liste ein; ein Klick öffnet den Node. Die Reiter über der Liste führen zu Kapazität,
Heatmap, Vergleich, Anomalien und Dashboards. Oben schlägt SerMon Module vor, wenn Agents passende Dienste gefunden
haben.

- [Flotte](../../help/de/pages/fleet.md)
- [Tags](../../help/de/concepts/tags.md)
- Kennzahlen: [CPU](../../help/de/figures/cpu_busy.md), [Arbeitsspeicher](../../help/de/figures/memory_used.md),
  [vollstes Dateisystem](../../help/de/figures/filesystem_max_used.md),
  [Load pro CPU](../../help/de/figures/load_per_cpu.md), [Uptime](../../help/de/figures/uptime.md),
  [Netzwerk](../../help/de/figures/network.md)

## 4. Die Node-Seite

Die Node-Seite zeigt alles zu einem Server: Host-Fakten, Charts nach Bereichen (CPU, Speicher, Datenträger, Netzwerk),
die Dateisysteme mit Prognose, wann sie voll sind, und Reiter für Prozesse (live), Logs, Security, Updates, Module und
Identität. Alle Charts teilen sich den Zeitraum oben; Ziehen zoomt, Doppelklick setzt zurück. Jeder Chart hat ein ⓘ
mit der Erklärung seiner Metrik und typischen Ursachen.

- [Node](../../help/de/pages/node.md)
- [Charts und Zeitraum](../../help/de/concepts/charts.md)
- [Live-Ansicht der Prozesse](../../help/de/concepts/live-view.md) und
  [Prozesse ohne Angaben](../../help/de/concepts/process-na.md)
- [Identität des Nodes](../../help/de/concepts/node-identity.md)
- [Quelle der Security-Events](../../help/de/concepts/security-source.md) und
  [unverifizierte Quelle](../../help/de/concepts/unverified-source.md)

## 5. Nodes hinzufügen, deaktivieren und löschen

**Node hinzufügen** (Operatoren) erzeugt für eine Gruppe einen Installationsbefehl mit einem Enrollment-Token. Auf dem
Server als root ausgeführt, installiert er den Agent, prüft dessen Signatur und meldet den Node an; Sekunden später
steht er in der Flotte.

Admins einer Gruppe deaktivieren einen Node (er sendet nichts mehr, seine Daten bleiben) oder löschen ihn (seine Daten
werden nach einer Frist gelöscht, die sich bis dahin abbrechen lässt).

- [Node hinzufügen](../../help/de/pages/add_node.md)
- [Deaktivieren und Löschen](../../help/de/concepts/node-lifecycle.md)

## 6. Alerts und Silences

Die Seite **Alerts** zeigt, was gerade feuert, mit Stufe, Node, Regel und Verlauf. Jeder Alert erklärt seine Ursache
und was zu tun ist – das ist der Hilfetext seiner Regel. **Quittieren** heißt: Jemand kümmert sich; Wiederholungen und
Eskalation hören auf. **Stummschalten** öffnet eine Silence für die Regel auf dem Node; für geplante Arbeiten legst
du eine Silence mit Kommentar und Ende an (bis zu 30 Tage). Silences halten nur Benachrichtigungen zurück, der Alert
bleibt sichtbar.

Im Reiter **Regeln** stehen die mitgelieferten Regeln mit ihren Schwellen; Admins schalten sie ab, passen sie an oder
importieren eigene (YAML). Der Reiter **Statistik** zeigt, welche Regeln am häufigsten feuern und wie schnell reagiert
wird.

- [Alerts](../../help/de/pages/alerts.md)
- [Alert-Statistik](../../help/de/concepts/alert-statistics.md), [Feuern](../../help/de/figures/alert_firings.md),
  [MTTA](../../help/de/figures/mtta.md), [MTTR](../../help/de/figures/mttr.md)

## 7. Benachrichtigungen

Unter **Benachrichtigungen** legen Admins Kanäle an (E-Mail, ntfy, Slack, Teams, Discord, Mattermost, Telegram, Gotify,
Pushover, Webhook) und mit Policies fest, welche Alerts wohin gehen, mit Gruppierung, Wiederholung und Eskalation.
Wartungsfenster schalten „Node offline“ und „Reboot“ jede Woche zur gleichen Zeit stumm. Das Protokoll zeigt jede
Zustellung der letzten 30 Tage.

- [Benachrichtigungen](../../help/de/pages/notifications.md)
- Kanäle: [E-Mail](../../help/de/channels/email.md), [ntfy](../../help/de/channels/ntfy.md),
  [Slack](../../help/de/channels/slack.md), [Teams](../../help/de/channels/teams.md),
  [Discord](../../help/de/channels/discord.md), [Mattermost](../../help/de/channels/mattermost.md),
  [Telegram](../../help/de/channels/telegram.md), [Gotify](../../help/de/channels/gotify.md),
  [Pushover](../../help/de/channels/pushover.md), [Webhook](../../help/de/channels/webhook.md)

## 8. Logs und Live-Ansicht

Die Seite **Logs** durchsucht die Log-Zeilen der Nodes aus dem systemd-Journal und aus Logdateien: Filter nach Node,
Gruppe, Quelle, Units, Schweregrad und Text (ohne reguläre Ausdrücke), ein Histogramm über den Zeitraum, der Kontext um
eine Zeile und **Live** für neue Zeilen, sobald sie ankommen. Alle Filter stehen in der Adresse; ein Link zeigt anderen
dieselbe Ansicht, soweit sie die Nodes sehen dürfen. Was ein Agent sendet und welche Geheimnisse er vorher unkenntlich macht, legen Admins der
Organisation fest. Ob Betrachter Logs sehen, entscheidet ebenfalls die Organisation.

- [Logs](../../help/de/pages/logs.md)
- [Log-Einstellungen und Redaction](../../help/de/concepts/log_settings.md)
- [Aufbewahrung](../../help/de/concepts/retention.md)

## 9. Security-Dashboard

Die Seite **Security** fasst die Anmeldungen auf den Nodes zusammen: fehlgeschlagene und erfolgreiche SSH-Logins,
Root-Logins, sudo-Fehler, Anmeldungen von neuen Quellen, dazu Top-IPs und Top-User. Filter und Zeitraum gelten für die
ganze Seite; die Liste lässt sich als CSV exportieren. Mitgelieferte Regeln melden etwa einen SSH-Brute-Force oder
einen Root-Login.

- [Security](../../help/de/pages/security_events.md)
- [Fehlgeschlagene Logins](../../help/de/figures/failed_logins.md),
  [neue Quellen](../../help/de/figures/new_source_logins.md), [Root-Logins](../../help/de/figures/root_logins.md),
  [sudo-Fehler](../../help/de/figures/sudo_failures.md)

## 10. Updates und Update-Policies

Die Seite **Updates** zeigt, welche Nodes Updates offen haben (Security-Updates zuerst) und ob ein Neustart fällig ist.
Operatoren starten Updates und Reboots für einzelne Nodes oder ganze Gruppen als Rollout; Admins legen je Gruppe eine
Update-Policy fest – nur melden, Security-Updates oder alle Updates in einem wöchentlichen Fenster – und können alle
Updates der Organisation pausieren. Agent-Updates laufen auf demselben Weg.

- [Updates](../../help/de/pages/updates.md)
- [Updates eines Nodes](../../help/de/concepts/updates.md)
- [Update-Policies](../../help/de/concepts/update-policy.md)

## 11. Module

Module überwachen Dienste eines Hosts: nginx, Apache, PostgreSQL, MySQL/MariaDB, Redis, Docker, systemd-Units,
TLS-Zertifikate, HTTP- und TCP-Checks, SMART und Prometheus-Exporter. Sie sind in den Agent eingebaut und ab Werk aus.
Findet ein Agent einen Dienst, schlägt SerMon das passende Modul vor; Admins schalten es je Gruppe ein und tragen
Zugangsdaten ein (verschlüsselt gespeichert, nie wieder angezeigt). Was ein Modul erreichen darf, legt der Betreiber
auf dem Host fest. Die Node-Seite zeigt im Reiter **Module**, wie es jedem Modul geht.

- [Module](../../help/de/concepts/modules.md)
- Einzelne Module: [nginx](../../help/de/modules/nginx.md), [Apache](../../help/de/modules/apache.md),
  [PostgreSQL](../../help/de/modules/postgres.md), [MySQL](../../help/de/modules/mysql.md),
  [Redis](../../help/de/modules/redis.md), [Docker (cgroups)](../../help/de/modules/docker.md),
  [Docker (Socket)](../../help/de/modules/docker_socket.md),
  [systemd-Units](../../help/de/modules/systemd_units.md), [TLS](../../help/de/modules/tls.md),
  [HTTP-Check](../../help/de/modules/http_check.md), [TCP-Check](../../help/de/modules/tcp_check.md),
  [SMART](../../help/de/modules/smart.md), [Prometheus](../../help/de/modules/prometheus.md)

## 12. Dashboards

Unter **Dashboards** baust du eigene Ansichten aus Widgets: Charts, Einzelwerte, Log-Zeilen und Alerts in einem Raster.
Mit Variablen wählst du über den Widgets einen Node oder eine Gruppe, so dient ein Dashboard für viele Nodes. Ein Dashboard gehört dir; geteilt sehen es alle
Mitglieder der Organisation – jeder aber nur mit den Daten, die seine Rolle erlaubt. Dashboards lassen sich als JSON
exportieren und importieren.

- [Dashboards](../../help/de/pages/dashboards.md)
- [Ein Dashboard bearbeiten](../../help/de/pages/dashboard.md)

## 13. Kiosk-Anzeigen

Für einen Bildschirm im Büro legen Admins an einem geteilten Dashboard einen Kiosk-Token an. Die Adresse `/kiosk#t=…`
zeigt das Dashboard ohne Anmeldung im Vollbild und nur lesend – mit nur dem, was der Admin, der den Token angelegt hat,
und der Eigentümer des Dashboards beide sehen dürfen. Ein Token läuft nach 1 bis 365 Tagen ab und lässt sich jederzeit
widerrufen. Die Adresse ist der Schlüssel: Wer sie hat, sieht das Dashboard.

- [Kiosk-Anzeigen](../../help/de/pages/dashboard.md) (Abschnitt „Kiosk-Anzeigen“)

## 14. Vergleich, Heatmap, Kapazität und Anomalien

- **Vergleich** legt zwei bis vier Nodes im selben Zeitraum übereinander – vor und nach einer Änderung oder innerhalb
  eines Clusters: [Vergleich](../../help/de/pages/compare.md).
- **Heatmap** zeigt alle Nodes über die Zeit, eine Zeile je Node: [Heatmap](../../help/de/pages/heatmap.md).
- **Kapazität** nennt Dateisysteme nach ihrer Prognose und Nodes, die mehr CPU und Speicher brauchen als vor einem
  Monat: [Kapazität](../../help/de/pages/capacity.md).
- **Anomalien** zeigt Nodes, deren Metriken von ihrem üblichen Wochenverlauf abweichen:
  [Anomalien](../../help/de/pages/anomalies.md), [wie die Erkennung arbeitet](../../help/de/concepts/anomalies.md).

## 15. Die Organisation verwalten

Admins für alle Gruppen verwalten unter **Organisation** Mitglieder und Rollen, Einladungen, Node-Gruppen, die Pflicht
zur Zwei-Faktor-Anmeldung, SSO-Gruppen, Log-Einstellungen, Module, die Aufbewahrung von Logs und Events und lesen das
Audit-Log. Die Kontingente setzt der Betreiber der Instanz; er verwaltet als Superadmin auch die Organisationen selbst.

- [Organisation](../../help/de/pages/admin.md)
- [Aufbewahrung](../../help/de/concepts/retention.md)
- [Organisationen der Instanz (Superadmin)](../../help/de/pages/organizations.md)
