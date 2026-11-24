# Root logins

Successful SSH logins directly as root in the range.

## Assessment

A direct root login bypasses the traceability of personal accounts: the log only says "root", not who it was.

## Next steps

1. Check whether every root login was expected (address, key).
2. Set `PermitRootLogin no` in `sshd_config` and work through personal accounts with sudo.
