#!/bin/sh
# Root helper package (M4-04), apk: an upgrade restarts a running helper, nothing else.
set -eu
rc-service --ifstarted sermon-helper restart >/dev/null 2>&1 || true
