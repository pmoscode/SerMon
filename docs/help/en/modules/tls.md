# TLS certificates

How long certificates have left, whether their chain leads to a trusted CA and whether the name fits – for certificate
files of the host and for TLS endpoints. The module is off by default and checks every 5 minutes; only these facts
leave the host, never a certificate or key.

## Requirements

- **Files** only below `module_file_prefixes` in `[local]` of `/etc/sermon/agent.toml` (by default `/etc/ssl`,
  `/etc/pki`, `/etc/letsencrypt`); symlinks are resolved and checked again. The agent runs as `sermon` and must be
  allowed to read the file. Certificates below `/etc/letsencrypt/live/` are mostly readable, the keys next to them
  are not – the module doesn't need them. If a file holds the key too (haproxy), the module reads only the
  certificates.
- **Endpoints** only within `module_targets` (by default loopback), names within `module_hosts` (by default
  `localhost`), ports within `module_ports` (by default 443, 465, 636, 993, 995, 8443 and those of the other modules).

## Settings

- **Certificate files:** PEM, the service's certificate first (e.g. `fullchain.pem`).
- **Endpoints:** `host:port`, e.g. `localhost:443`.
- **Server name:** the name sent to the endpoints and checked in their certificates; empty means the host of each
  endpoint. For `127.0.0.1:443` with a certificate for `example.org`, enter `example.org` here.
- **CA file:** checks the chains against these CAs instead of the system's (an own CA).

## Metrics

- `sermon_tls_cert_expires_in_seconds`, `sermon_tls_chain_expires_in_seconds`: time left of the certificate and of the
  whole chain.
- `sermon_tls_chain_valid`: the chain leads to a trusted CA (an expired certificate doesn't count here).
- `sermon_tls_name_valid`: endpoints only, the certificate is valid for the name.
- `sermon_tls_up`: certificates were read.

## When it doesn't work

- State `vetoed`: the file or endpoint lies outside the allowed paths or networks.
- `sermon_tls_up` 0 for a file: it is missing, not readable (permissions of `sermon`) or no PEM.
