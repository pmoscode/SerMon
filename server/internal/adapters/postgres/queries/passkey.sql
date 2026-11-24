-- Passkeys (M3-23, ADR 0034): instance-level, like the credentials.

-- name: ListPasskeys :many
SELECT id, user_id, name, credential, created_at, last_used_at FROM webauthn_credentials
WHERE user_id = sqlc.arg(user_id) ORDER BY created_at, id;

-- name: FindPasskey :one
SELECT id, user_id, name, credential, created_at, last_used_at FROM webauthn_credentials WHERE id = sqlc.arg(id);

-- name: LockUserPasskeys :exec
-- Serializes adding passkeys of a user (the limit holds under parallel registrations).
SELECT pg_advisory_xact_lock(hashtextextended('passkeys:' || sqlc.arg(user_id)::uuid::text, 0));

-- name: CountPasskeys :one
SELECT count(*) FROM webauthn_credentials WHERE user_id = sqlc.arg(user_id);

-- name: InsertPasskey :execrows
INSERT INTO webauthn_credentials (id, user_id, name, credential, created_at)
VALUES (sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(credential), sqlc.arg(created_at))
ON CONFLICT (id) DO NOTHING;

-- name: PasskeyUsed :exec
UPDATE webauthn_credentials SET credential = sqlc.arg(credential), last_used_at = sqlc.arg(at) WHERE id = sqlc.arg(id);

-- name: RenamePasskey :execrows
UPDATE webauthn_credentials SET name = sqlc.arg(name) WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: DeletePasskey :one
DELETE FROM webauthn_credentials WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) RETURNING name;

-- name: InsertWebAuthnCeremony :execrows
-- Only while fewer than max_open ceremonies are open (M3-29).
INSERT INTO webauthn_ceremonies (id_hash, binding_hash, purpose, user_id, session_id, name, data, created_at, expires_at)
SELECT sqlc.arg(id_hash), sqlc.narg(binding_hash)::text, sqlc.arg(purpose), sqlc.narg(user_id)::uuid, sqlc.narg(session_id)::text,
       sqlc.arg(name), sqlc.arg(data), sqlc.arg(created_at), sqlc.arg(expires_at)
WHERE (SELECT count(*) FROM webauthn_ceremonies) < sqlc.arg(max_open)::bigint;

-- name: TakeWebAuthnCeremony :one
-- Once: the ceremony is deleted whether it expired or not.
DELETE FROM webauthn_ceremonies WHERE id_hash = sqlc.arg(id_hash)
RETURNING binding_hash, purpose, user_id, session_id, name, data, created_at, expires_at;

-- name: DeleteExpiredWebAuthnCeremonies :exec
DELETE FROM webauthn_ceremonies WHERE expires_at <= sqlc.arg(now);
