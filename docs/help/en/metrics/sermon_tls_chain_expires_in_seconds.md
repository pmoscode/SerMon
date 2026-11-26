# Chain expires in

Time until the first certificate of the chain expires, intermediates included. Less than the certificate's own time if an intermediate expires earlier.

## Reading it

From the module `tls`; the label `target` is the configured file or `host:port`. Files only below the host's `module_file_prefixes`, endpoints only within its `module_targets`, `module_hosts` and `module_ports`.

Metric: `sermon_tls_chain_expires_in_seconds`
