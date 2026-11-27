# Agent CA

The internal certificate authority that issues the certificates of the agents and of the agent API – for superadmins only. Its intermediate CA is valid for two years and doesn't renew itself: before it expires, a rollover replaces it with a new one.

## Expiry and warning

At the top: when the first trusted CA expires. From 90 days before, SerMon warns all superadmins by mail and in the band below the header, from 30 days as critical. After the expiry agents and server refuse each other, and every host must be enrolled again.

## Rollover

1. **Start the rollover** (with a renewed confirmation): a new CA issues all certificates from now on. The agents receive both roots when they connect next and renew their certificate with the new CA at their next rotation. Existing connections keep running, new hosts enroll as usual.
2. **Wait:** the previous CA shows how many valid certificates it still has. 30 days after the start at the latest all are replaced or expired; hosts that stay offline that long must be enrolled again afterwards.
3. **Retire the previous CA** (with a renewed confirmation) once it has no certificates left. From then on the agent API's certificate comes from the new CA, too.

So start the rollover 30 days before the expiry at the latest, earlier is better. Both steps are in the instance audit log.

## Fingerprint

The SHA-256 of each CA's root certificate. It tells which CA a host trusts.
