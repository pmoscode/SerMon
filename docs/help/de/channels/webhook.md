# Webhook

SerMon schickt jede Benachrichtigung als JSON per POST an deine URL, signiert mit einem gemeinsamen Geheimnis.

## Einrichten

- **URL:** Ziel der Requests, über https. Interne Adressen (privat, Loopback, Link-local) sind gesperrt, außer der Betreiber gibt sie in `SERMON_OUTBOUND_ALLOWLIST` frei; nur dann geht auch http.
- **Geheimnis:** mindestens 16 Zeichen, am besten zufällig, z. B. mit `openssl rand -hex 32`. SerMon speichert es verschlüsselt und zeigt es nie wieder an.

## Signatur prüfen

Jeder Request hat den Header `X-SerMon-Signature: t=<Zeitstempel>,v1=<Signatur>`. Die Signatur ist HMAC-SHA256 über `<Zeitstempel>.<Body>` mit dem Geheimnis, als Hex.

1. Zeitstempel und Signatur aus dem Header lesen.
2. HMAC-SHA256 über den Zeitstempel, einen Punkt und den unveränderten Body berechnen.
3. In konstanter Zeit vergleichen.
4. Requests mit einem Zeitstempel älter als 5 Minuten ablehnen (Schutz gegen Wiederholung).

```
import hmac, hashlib, time

def verify(secret: bytes, header: str, body: bytes) -> bool:
    parts = dict(p.split("=", 1) for p in header.split(","))
    expected = hmac.new(secret, parts["t"].encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, parts["v1"]) and abs(time.time() - int(parts["t"])) < 300
```

## Inhalt

Der Body enthält unter anderem `kind` (firing, resolved, acknowledged, flapping_started, flapping_stopped, test), `severity`, `rule`, `node`, `dimension`, `value`, `fields` (bei Security-Regeln z. B. User und IP), `title`, `text` und `link`. Der Header `X-SerMon-Event` nennt die Art. Werte wie Host- und User-Namen kommen von den überwachten Servern: Gib sie nur escaped aus.

## Zustellung

Antwortet die URL mit 2xx, gilt die Nachricht als zugestellt. Bei 5xx, 408 oder einem Netzwerkfehler versucht SerMon es bis zu sechsmal mit wachsendem Abstand, bei 429 nach der Zeit aus `Retry-After`. Andere Antworten gelten als endgültiger Fehler. Eine Nachricht kann im Ausnahmefall doppelt ankommen.
