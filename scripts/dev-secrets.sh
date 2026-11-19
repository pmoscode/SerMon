#!/bin/sh
# Creates random secrets for the development infrastructure (deploy/dev/.secrets/, git-ignored).
# Existing secrets are kept, so the database password stays valid across restarts.
set -eu

dir="$(cd "$(dirname "$0")/.." && pwd)/deploy/dev/.secrets"
mkdir -p "$dir"
chmod 700 "$dir"

secret() {
  file="$dir/$1"
  if [ ! -s "$file" ]; then
    # 32 random bytes, hex encoded.
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$file"
    # Readable inside the container (bind mount); the directory itself is private.
    chmod 644 "$file"
    echo "created $file"
  fi
}

secret postgres_password
secret postgres_app_password
