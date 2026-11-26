# Certificate read

1 if certificates were read (file) or presented (endpoint), else 0 – the file is missing or no PEM, or the endpoint doesn't answer.

## Reading it

From the module `tls`; the label `target` is the configured file or `host:port`. Files only below the host's `module_file_prefixes`, endpoints only within its `module_targets`.

Metric: `sermon_tls_up`
