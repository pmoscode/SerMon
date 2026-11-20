# Add node

Adds a Linux server with one command: choose the group, create the command and run it on the server as root.

## Steps

1. Choose the group, validity and number of uses. The command contains an **enrollment token** that is shown only now.
2. Run the command on the server. The script checks the agent's **signature**, creates the `sermon` user, sets up the service and enrolls the server.
3. This page shows when the server is connected and the first metrics arrive.

## Good to know

- “View the script first” opens the install script as text.
- **Without a publicly trusted certificate** (self-signed, own CA) tick the box and enter the fingerprint of the server certificate or of a CA the server sends along (e.g. the intermediate CA of Caddy `tls internal`). The command then downloads `install.sh` without a certificate check but checks it with `sha256sum` against the checksum your browser got from the server, before sudo runs it. The script checks the agent's signature in any case.
- The token is in a variable (`export`), not on the command line: other users of the server don't see it in the process list. The leading space keeps it out of the bash history.
- The script only installs an agent signed for exactly the version of this server.
- Nodes always belong to a group. If there is none yet, admins create one under “Organization” → “Groups”.
- The token is stored only as a hash and works at most as often and as long as chosen.
- A token lets servers join the organization. If your last sign-in was more than 15 minutes ago, SerMon first asks for your password or second factor.
- The server doesn't show up? Can it reach the server on port 443 and the agent port (default 8443)? The script's output names the reason.
