#!/bin/sh
# Creates the login role sermon-server uses (ADR 0007): no superuser, no BYPASSRLS, owns nothing.
# Runs once when the postgres volume is initialized. The TimescaleDB extension itself is created
# by the image's own init script; schema objects come from the server's migrations.
set -eu

password="$(cat /run/secrets/postgres_app_password)"
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v app_password="$password" <<'SQL'
-- :'app_password' is a psql variable (quoted by psql), not a secret.
CREATE ROLE sermon_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
  PASSWORD :'app_password'; -- betterleaks:allow
SQL
