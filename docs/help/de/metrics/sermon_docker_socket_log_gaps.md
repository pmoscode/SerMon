# Lücken in Container-Logs

Wie viele der Container unter **Container-Logs** in diesem Lauf Zeilen verloren haben: Die Antwort hatte die vollen
1 000 Zeilen (ältere hat Docker abgeschnitten), oder die Ausgabe ließ sich auch mit 100 Zeilen nicht lesen (zu groß,
ein Fehler) – dann springt das Modul auf die Zeit des Laufs.

## Einordnung

Vom Modul `docker_socket`, nur mit Container-Logs. Dauerhaft über 0 heißt: Ein Container schreibt mehr, als die
Grenzen erlauben (312 Zeilen je Lauf, die halbe Log-Rate des Nodes). Abhilfe: das Intervall des Moduls verkürzen, den
Container leiser stellen oder ihn aus den Container-Logs nehmen.

Metrik: `sermon_docker_socket_log_gaps`
