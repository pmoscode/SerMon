# Worker-Plätze

Plätze im Scoreboard (`ServerLimit` × Threads je Prozess): so viele Anfragen kann Apache gleichzeitig bearbeiten. Erreichen die beschäftigten Worker diesen Wert, stauen sich Anfragen.

## Einordnung

Vom Modul `apache` (`server-status?auto`). Raten ab dem zweiten Lauf; nach einem Neustart von Apache setzen sie einen Lauf aus.

Metrik: `sermon_apache_workers_max`
