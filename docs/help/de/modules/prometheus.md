# Prometheus-Scraper

Übernimmt Serien von `/metrics`-Endpunkten (Prometheus-Textformat und OpenMetrics), etwa einer eigenen Anwendung oder
eines Exporters. Das Modul ist ab Werk aus.

## Wie die Serien ankommen

SerMon kennt jede Metrik vorab (Katalog); fremde Namen werden deshalb keine eigenen Metriken. Jede Serie landet in
`sermon_prometheus_gauge` (Werte) oder `sermon_prometheus_rate` (Zähler je Sekunde), der fremde Name im Label `name`,
seine Labels im Label `series`. So kollidiert nichts mit den Metriken des Agents. Buckets von Histogrammen und
`_created` werden ausgelassen.

## Voraussetzungen

Nur Endpunkte unter `module_targets` in `[local]` von `/etc/sermon/agent.toml` (ab Werk Loopback), Namen unter
`module_hosts` (ab Werk `localhost`) und Ports unter `module_ports` (ab Werk unter anderem 9090–9999, die Exporter). Höchstens 1 MiB je
Antwort, höchstens drei Weiterleitungen innerhalb der erlaubten Ziele.

## Einstellungen

- **Endpunkte:** bis zu 8 URLs.
- **Metriken:** Namen, die übernommen werden, `prefix*` für alle mit einem Anfang; leer heißt alle bis zur Grenze.
  Besser eng fassen: Exporter wie der node exporter liefern Tausende Serien.
- **Serien je Endpunkt:** ab Werk 50, alle Endpunkte zusammen höchstens 250. Die Plätze sind fest: Eine Serie, die
  einmal einen Platz hat, behält ihn; eine neue bekommt erst einen, wenn eine bekannte eine Stunde lang fehlt. So füllt
  ein Exporter, dessen Labels sich mit jeder Anfrage ändern (Pfade, User-IDs), nicht das Serien-Budget des Nodes; die
  übrigen zählt `sermon_prometheus_dropped_series`.
- **Bearer-Token:** geht als `Authorization: Bearer …` nur an die eingestellten Endpunkte, nie nach einer
  Weiterleitung an einen anderen Host; es liegt beim Agent nur im Speicher (M5-05). Über `http://` geht es im
  Klartext an jeden, der dort lauscht – bei gestopptem Exporter auch an einen anderen lokalen Benutzer, der den Port
  belegt. Mit Token deshalb `https://` mit geprüftem Zertifikat und ein Token, das nur `/metrics` lesen darf.
- **Timeout**, **TLS prüfen**, **CA-Datei**.

## Metriken

- `sermon_prometheus_up`, `sermon_prometheus_series`, `sermon_prometheus_dropped_series` je Endpunkt.
- `sermon_prometheus_gauge`, `sermon_prometheus_rate` mit `name` und `series`.

## Wenn es nicht läuft

- „not the Prometheus text format“: Unter der URL antwortet etwas anderes.
- `sermon_prometheus_dropped_series` über 0: Grenze erreicht oder Labels über 256 Byte.
