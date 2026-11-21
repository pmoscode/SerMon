# E-mail

Notifications as e-mail with a text and an HTML part, sent through the mail server of the SerMon installation.

## Setup

- **Recipients:** up to 10 addresses separated by commas, e.g. `ops@example.org, oncall@example.org`. Each recipient gets a mail of their own and doesn't see the others.

The operator sets the mail server (`SERMON_SMTP_*`, always with TLS). Without it this channel does not exist.

## Delivery

If the mail server rejects an address for good (error 5xx), SerMon gives up; temporary errors (4xx, network) it retries up to six times. If mails don't arrive, check the spam folder and whether the sender address of the installation is allowed by SPF and DKIM.
