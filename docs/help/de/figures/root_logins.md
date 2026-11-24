# Root-Logins

Erfolgreiche SSH-Anmeldungen direkt als root im Zeitraum.

## Einordnung

Ein direkter Root-Login umgeht die Nachvollziehbarkeit persönlicher Konten: Im Log steht nur „root“, nicht wer es war.

## Nächste Schritte

1. Prüfen, ob jeder Root-Login erwartet war (Adresse, Schlüssel).
2. `PermitRootLogin no` in der `sshd_config` setzen und über persönliche Konten mit sudo arbeiten.
