-- OIDC (M3-20, ADR 0033): instance-level, like the credentials.

-- name: InsertOIDCFlow :exec
INSERT INTO oidc_flows (state_hash, binding_hash, nonce, verifier, intent, user_id, session_id, next_path, created_at, expires_at)
VALUES (sqlc.arg(state_hash), sqlc.arg(binding_hash), sqlc.arg(nonce), sqlc.arg(verifier), sqlc.arg(intent), sqlc.narg(user_id),
        sqlc.narg(session_id), sqlc.arg(next_path), sqlc.arg(created_at), sqlc.arg(expires_at));

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
