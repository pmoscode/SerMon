#!/usr/bin/env bash
# Installs Chromium and its system packages for Playwright in CI. The Ubuntu mirrors of the
# GitHub runners fail now and then (HTTP 503); retry with a pause instead of failing the run.
set -euo pipefail

attempts=3
for i in $(seq 1 "$attempts"); do
  if pnpm exec playwright install --with-deps chromium; then
    exit 0
  fi
  if [ "$i" -lt "$attempts" ]; then
    echo "::warning::playwright install failed (attempt $i/$attempts), retrying in $((i * 60)) s"
    sleep $((i * 60))
  fi
done
echo "::error::playwright install failed after $attempts attempts"
exit 1
