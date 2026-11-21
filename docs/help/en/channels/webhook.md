# Webhook

SerMon posts every notification as JSON to your URL, signed with a shared secret.

## Setup

- **URL:** target of the requests, over https. Internal addresses (private, loopback, link-local) are blocked unless the operator allows them in `SERMON_OUTBOUND_ALLOWLIST`; only then plain http works as well.
- **Secret:** at least 16 characters, ideally random, e.g. from `openssl rand -hex 32`. SerMon stores it encrypted and never shows it again.

## Verifying the signature

Every request carries the header `X-SerMon-Signature: t=<timestamp>,v1=<signature>`. The signature is HMAC-SHA256 over `<timestamp>.<body>` with the secret, in hex.

1. Read timestamp and signature from the header.
2. Compute HMAC-SHA256 over the timestamp, a dot and the unchanged body.
3. Compare in constant time.
4. Reject requests whose timestamp is older than 5 minutes (protection against replays).

```
import hmac, hashlib, time

def verify(secret: bytes, header: str, body: bytes) -> bool:
    parts = dict(p.split("=", 1) for p in header.split(","))
    expected = hmac.new(secret, parts["t"].encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, parts["v1"]) and abs(time.time() - int(parts["t"])) < 300
```

## Content

The body contains among others `kind` (firing, resolved, acknowledged, flapping_started, flapping_stopped, test), `severity`, `rule`, `node`, `dimension`, `value`, `fields` (for security rules e.g. user and IP), `title`, `text` and `link`. The header `X-SerMon-Event` names the kind. Values like host and user names come from the monitored servers: output them escaped only.

## Delivery

A 2xx answer counts as delivered. On 5xx, 408 or a network error SerMon tries up to six times with growing pauses, on 429 after the time in `Retry-After`. Other answers are final errors. In rare cases a message may arrive twice.
