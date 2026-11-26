# Name valid

Endpoints only: 1 if the certificate is valid for the host (or the configured server name), else 0. An endpoint given by IP address needs the IP in the certificate or a server name.

## Reading it

From the module `tls`; the label `target` is the configured file or `host:port`. Files only below the host's `module_file_prefixes`, endpoints only within its `module_targets`.

Metric: `sermon_tls_name_valid`
