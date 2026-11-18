---
name: sermon-add-notifier
description: Implement a new alert notification channel in the SerMon Go server (e.g. Slack, Teams, Discord, Mattermost, Telegram, ntfy, Gotify, Pushover, e-mail, webhook). Use when asked to add or fix a notification integration.
---

# Add a notification channel

Reference: KONZEPT.md §12.4, §18.5 (secrets), §18.6 (SSRF).
Package: `server/internal/adapters/notify/<channel>/`.

## Contract
```go
type Notifier interface {
    Type() ChannelType
    ValidateConfig(cfg json.RawMessage) error           // shape + semantic validation
    Send(ctx context.Context, cfg Config, n Notification) error
}
```
- `Notification` contains: alert state, severity, rule name/help (localized), node, labels, `notifyFields`, deep link, timestamps. Language is chosen per channel.

## Steps (TDD – `sermon-tdd`)
1. **Config schema:** define a struct + JSON schema (drives the UI form). Mark secret fields with `secret:"true"` so they're stored via `secrets.Box`, masked in API responses and never logged.
2. **Validation tests first:** missing/invalid URL, token format, chat IDs, etc.
3. **Payload rendering:** pure function `render(n Notification, lang) (Payload, error)`. Golden-file tests in `testdata/` for firing, resolved, acknowledged, flapping; DE + EN. Escape per target format (Markdown/HTML/JSON) – alert data is untrusted (hostnames, usernames, log excerpts).
4. **Transport:** use only the shared `safehttp.Client` (SSRF protection, IP pinning, timeouts, max response size). Test with `httptest.Server` via the allowlist hook; assert headers, body, signature, retry on 429/5xx with backoff, no retry on 4xx.
5. **Rate limits:** respect provider limits (e.g. Telegram, Discord) – return a typed `ErrRateLimited{RetryAfter}`.
6. **Register** in the notifier registry; add "Send test notification" support (uses a synthetic notification).
7. **UI:** form is generated from the schema; add i18n labels + help text (`docs/help/{de,en}/channels/<channel>.md`) explaining how to obtain the webhook/token.

## Checklist
- [ ] No direct `http.Client` usage; `safehttp` only
- [ ] Secrets marked & encrypted; none in logs/errors
- [ ] Golden files DE + EN; escaping tested with malicious input
- [ ] Delivery status recorded (success/error text) for the UI
- [ ] `sermon-security-review` §5 + §6 passed
