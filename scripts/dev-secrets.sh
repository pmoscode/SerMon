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
secret postgres_owner_password
secret postgres_app_password
secret postgres_backup_password

# Connection strings for sermon-server (read via SERMON_DATABASE_URL_FILE / _MIGRATE_URL_FILE /
# _BACKUP_URL_FILE).
port="${SERMON_DEV_PG_PORT:-55432}"
url() { # file user password-file
  printf 'postgres://%s:%s@127.0.0.1:%s/sermon?sslmode=disable' "$2" "$(cat "$dir/$3")" "$port" >"$dir/$1"
  chmod 600 "$dir/$1"
}
url database_url sermon_app postgres_app_password
url database_migrate_url sermon postgres_password
url database_backup_url sermon_backup postgres_backup_password

# Master key for sermon-server (format "<id>:<base64 of 32 bytes>"); never replaced, because
# data encrypted with it (agent CA, channel secrets) would become unreadable.
keys="$dir/master_keys"
if [ ! -s "$keys" ]; then
  umask 077
  printf '1:%s\n' "$(head -c 32 /dev/urandom | base64 | tr -d '\n')" >"$keys"
  echo "created $keys"
fi
