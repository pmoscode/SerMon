# Freie Worker

Worker, die auf eine Anfrage warten (`IdleWorkers`). Bleiben keine frei, warten neue Anfragen.

## Einordnung

Vom Modul `apache` (`server-status?auto`). Raten ab dem zweiten Lauf; nach einem Neustart von Apache setzen sie einen Lauf aus.

Metrik: `sermon_apache_workers_idle`
