# Update-Policy

Was eine Gruppe mit ihren Updates selbst tut: nur melden, Security-Updates oder alle Updates in einem wöchentlichen
Fenster installieren, auf Wunsch mit Neustart.

## Modus und Fenster

- **nur melden:** Updates kommen nur von Hand.
- **Security-Updates automatisch:** im Fenster die Security-Updates. Alpine kennt keine Security-Updates; seine Nodes
  melden dann nur.
- **alle Updates automatisch:** im Fenster alle Updates.

Das Fenster beginnt an den gewählten Tagen zur Ortszeit der Zeitzone und dauert 15 Minuten bis 24 Stunden. Nach dem
Fensterende startet kein Node mehr; laufende werden fertig.

## Rollout

- **Gleichzeitig:** wie viele Nodes der Gruppe zugleich aktualisiert werden (1 bis 50), nach Hostname.
- **Neustart nach Updates:** nur wenn der Node ihn braucht, der Host ihn erlaubt (`allow_remote_reboot`) und das
  Fenster offen ist.
- **Health-Check:** In dieser Zeit muss der Node wieder online sein – nach einem Neustart mit einer neuen Verbindung –
  und eine Minute lang ohne kritischen Alert.
- **Beim ersten Fehlschlag stoppen:** Danach startet kein weiterer Node; das Ereignis „Rollout gestoppt“ entsteht.

Ändern braucht Admin-Rechte auf allen Gruppen und eine erneute Anmeldung; jede Änderung steht im Audit-Log. Als YAML
lässt sich die Policy aus- und einlesen (`kind: UpdatePolicy`).
