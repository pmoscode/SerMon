# Chain valid

1 if the chain leads to a CA of the system (or of the configured CA file), else 0 – mostly an intermediate is missing or the certificate is self-signed. An expired certificate doesn't count here: its expiry shows that.

## Reading it

From the module `tls`; the label `target` is the configured file or `host:port`. Files only below the host's `module_file_prefixes`, endpoints only within its `module_targets`.

Metric: `sermon_tls_chain_valid`
