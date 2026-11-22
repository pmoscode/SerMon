# Identität des Nodes

Jeder Agent meldet sich mit einem eigenen Schlüssel und Zertifikat; hier lässt es sich erneuern, widerrufen, entsperren und für eine Neuinstallation übergeben.

## Gesperrt

Verbindet sich derselbe Node zweimal gleichzeitig von verschiedenen Adressen, oder rotiert er seinen Schlüssel von einer neuen Adresse, sperrt der Server ihn vorsorglich: Jemand könnte den Schlüssel kopiert haben. Der Sperrgrund nennt den Anlass.

- **Entsperren**, wenn die Erklärung harmlos ist (etwa ein geklonter Container, der wieder gelöscht ist).
- **Schlüssel widerrufen**, wenn der Schlüssel gestohlen sein könnte: Alle Zertifikate werden ungültig, der Agent muss mit einem neuen Token installiert werden. Der gerade verbundene Agent bekommt keinen neuen Schlüssel – er könnte der Dieb sein.

## Schlüssel rotieren

Ein verbundener Agent bekommt sofort einen neuen Schlüssel, der alte wird gesperrt. Ist der Node nicht verbunden, werden seine Zertifikate gesperrt und er muss neu installiert werden.

## Übernahme

Wird ein Host neu installiert, behält er meist seine `machine-id`; eine neue Installation wird dann abgelehnt, damit niemand einen fremden Node übernimmt. **Übernahme erlauben** öffnet für eine Stunde ein Fenster: Die nächste Installation mit derselben `machine-id` und einem Token der Gruppe dieses Nodes übernimmt ihn. Er behält seine Historie, seine alten Zertifikate werden ungültig.

Alle Aktionen brauchen Operator-Rechte in der Gruppe und eine frische Bestätigung des Passworts; sie stehen im Audit-Log.
