# Retention

How long this organization keeps its logs and security events – other than the instance's default, for example
shorter because logs contain personal data, or events longer for later investigations. Admins of all groups set it after a fresh confirmation; every change is in the audit log.

## Limits

- **Logs:** 1 to 14 days; empty means 14 days like the instance. Longer is not possible because Loki deletes
  everything after 14 days.
- **Security events and alert history:** 7 to 365 days; empty means the instance's default (90 days unless the
  operator changed it; the value is shown below the field). Less than 7 days is not possible: agents buffer their
  events that long while the server is out of reach. An attack on SSH makes one row per attempt – a long period costs
  a lot of space under many attacks.
- If the operator set a **cap** for this organization, a setting of its own stays below it. A lowered cap leaves an
  existing setting as it is.
- Metrics follow the instance's retention (10 s values 24 h, minutes 30 days, hours a year).

## When it is deleted

- **Events** older than the period are deleted by the server every hour.
- **Logs** are deleted by Loki: once a day the server records a deletion of all lines before the period, Loki runs it
  after about 10 minutes. Logs may therefore stay visible for up to a day longer.
