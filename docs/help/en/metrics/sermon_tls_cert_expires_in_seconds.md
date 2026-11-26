# Certificate expires in

Time until the first certificate (the service's) expires; negative once expired. Renew in time, e.g. 21 days ahead.

## Reading it

From the module `tls`; the label `target` is the configured file or `host:port`. Files only below the host's `module_file_prefixes`, endpoints only within its `module_targets`.

Metric: `sermon_tls_cert_expires_in_seconds`
