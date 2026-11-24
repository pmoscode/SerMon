# Fehlgeschlagene Logins

SSH-Anmeldungen mit falschem Passwort oder Schlüssel und Versuche mit unbekanntem User im Zeitraum.

## Einordnung

- Auf Servern mit offenem SSH-Port aus dem Internet sind ständige Fehlversuche normal: Bots probieren Listen von Namen und Passwörtern.
- Auffällig sind Spitzen, eine Adresse auf mehreren Nodes oder Versuche auf internen Nodes, die von außen nicht erreichbar sein sollten.

## Nächste Schritte

1. Passwort-Logins abschalten (`PasswordAuthentication no`) und nur Schlüssel erlauben.
2. SSH nur aus bekannten Netzen erreichbar machen (Firewall, VPN).
3. Eine Adresse, die es auf vielen Nodes versucht, sperren.
