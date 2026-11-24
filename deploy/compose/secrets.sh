#!/bin/sh
# Creates the secrets of the production compose in ./secrets (M2-26, M2-36): random database
# passwords (superuser, schema owner, sermon_app) and the two database URLs built from them.
# Existing files are never overwritten – a new password would lock the server out of its
# database. smtp_password and oidc_client_secret are created empty; put the password of
# SERMON_SMTP_USERNAME and the client secret of SERMON_OIDC_CLIENT_ID there.
#
#   ./secrets.sh [DIR]     default: secrets/ next to this script
set -eu

dir=${1:-"$(cd "$(dirname "$0")" && pwd)/secrets"}
umask 077
mkdir -p "$dir"
chmod 0700 "$dir"

# create NAME CONTENT: writes the file unless it exists.
create() {
	if [ -e "$dir/$1" ]; then
		echo "kept     $dir/$1"
	else
		printf '%s' "$2" >"$dir/$1"
		echo "created  $dir/$1"
	fi
}

random() { openssl rand -hex 24 | tr -d '\n'; }

# postgres_password: the superuser, for the database container only (M2-36).
create postgres_password "$(random)"
create postgres_owner_password "$(random)"
create postgres_app_password "$(random)"
create database_url "postgres://sermon_app:$(cat "$dir/postgres_app_password")@postgres:5432/sermon?sslmode=disable"
create database_migrate_url "postgres://sermon_owner:$(cat "$dir/postgres_owner_password")@postgres:5432/sermon?sslmode=disable"
create smtp_password ""
# oidc_client_secret: empty without OIDC; the client secret of SERMON_OIDC_CLIENT_ID otherwise (M3-20).
create oidc_client_secret ""

# The containers run as their own users (postgres, nonroot) and read the files through bind
# mounts; the directory itself stays private to the owner.
chmod 0644 "$dir"/*
