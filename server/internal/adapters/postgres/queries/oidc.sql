-- OIDC (M3-20, ADR 0033): instance-level, like the credentials.

-- name: InsertOIDCFlow :execrows
-- Only while fewer than max_open flows are open (M3-29).
INSERT INTO oidc_flows (state_hash, binding_hash, nonce, verifier, intent, user_id, session_id, next_path, created_at, expires_at)
SELECT sqlc.arg(state_hash), sqlc.arg(binding_hash), sqlc.arg(nonce), sqlc.arg(verifier), sqlc.arg(intent), sqlc.narg(user_id)::uuid,
       sqlc.narg(session_id)::text, sqlc.arg(next_path), sqlc.arg(created_at), sqlc.arg(expires_at)
WHERE (SELECT count(*) FROM oidc_flows) < sqlc.arg(max_open)::bigint;

-- name: TakeOIDCFlow :one
-- Once: the flow is deleted whether it expired or not.
DELETE FROM oidc_flows WHERE state_hash = sqlc.arg(state_hash)
RETURNING binding_hash, nonce, verifier, intent, user_id, session_id, next_path, created_at, expires_at;

-- name: DeleteExpiredOIDCFlows :exec
DELETE FROM oidc_flows WHERE expires_at <= sqlc.arg(now);

-- name: FindIdentity :one
SELECT user_id FROM user_identities WHERE issuer = sqlc.arg(issuer) AND subject = sqlc.arg(subject);

-- name: InsertIdentity :execrows
-- Nothing if the identity belongs to someone or the user has one of the issuer.
INSERT INTO user_identities (issuer, subject, user_id, email)
VALUES (sqlc.arg(issuer), sqlc.arg(subject), sqlc.arg(user_id), sqlc.narg(email))
ON CONFLICT DO NOTHING;

-- name: IdentitySeen :exec
UPDATE user_identities SET last_login_at = sqlc.arg(at), email = coalesce(sqlc.narg(email), email)
WHERE issuer = sqlc.arg(issuer) AND subject = sqlc.arg(subject);

-- name: IdentitiesOfUser :many
SELECT issuer, subject, coalesce(email, '')::text AS email, created_at, last_login_at
FROM user_identities WHERE user_id = sqlc.arg(user_id) ORDER BY created_at, issuer;

-- name: DeleteIdentity :one
DELETE FROM user_identities WHERE user_id = sqlc.arg(user_id) AND issuer = sqlc.arg(issuer) RETURNING subject;

-- name: HasPassword :one
SELECT EXISTS (SELECT 1 FROM user_credentials WHERE user_id = sqlc.arg(user_id)) AS has_password;
