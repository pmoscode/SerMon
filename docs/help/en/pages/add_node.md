# Add node

Adds a Linux server with one command: choose the group, create the command and run it on the server as root.

## Steps

1. Choose the group, validity and number of uses. The command contains an **enrollment token** that is shown only now.
2. Run the command on the server. The script checks the agent's **signature**, creates the `sermon` user, sets up the service and enrolls the server.
3. This page shows when the server is connected and the first metrics arrive.

## Good to know

- “View the script first” opens the install script as text.
- With a self-signed certificate append `--ca-fingerprint sha256:…` (fingerprint of the server certificate).
- The token is stored only as a hash and works at most as often and as long as chosen.
- The server doesn't show up? Can it reach the server on port 443 and the agent port (default 8443)? The script's output names the reason.
