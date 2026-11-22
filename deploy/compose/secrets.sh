#!/bin/sh
# Creates the secrets of the production compose in ./secrets (M2-26): random database passwords
# and the two database URLs built from them. Existing files are never overwritten – a new password
# would lock the server out of its database. smtp_password is created empty; put the password of
# SERMON_SMTP_USERNAME there.
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

create postgres_password "$(random)"
create postgres_app_password "$(random)"
create database_url "postgres://sermon_app:$(cat "$dir/postgres_app_password")@postgres:5432/sermon?sslmode=disable"
create database_migrate_url "postgres://sermon:$(cat "$dir/postgres_password")@postgres:5432/sermon?sslmode=disable"
create smtp_password ""

# The containers run as their own users (postgres, nonroot) and read the files through bind
# mounts; the directory itself stays private to the owner.
chmod 0644 "$dir"/*
