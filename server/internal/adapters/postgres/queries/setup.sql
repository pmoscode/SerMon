-- First installation: setup token and superadmins (instance-level tables, no RLS).

-- name: SetupCompleted :one
SELECT EXISTS (SELECT 1 FROM instance_setup WHERE completed_at IS NOT NULL) AS completed;

-- Replaces a previous token; does nothing once the setup is completed.
-- name: IssueSetupToken :execrows
INSERT INTO instance_setup (singleton, token_hash, token_expires_at)
VALUES (true, sqlc.arg(token_hash), sqlc.arg(expires_at))
ON CONFLICT (singleton) DO UPDATE
    SET token_hash = EXCLUDED.token_hash, token_expires_at = EXCLUDED.token_expires_at
    WHERE instance_setup.completed_at IS NULL;

-- name: ClaimSetupToken :execrows
UPDATE instance_setup
SET completed_at = sqlc.arg(now), token_hash = NULL, token_expires_at = NULL
WHERE token_hash = sqlc.arg(token_hash) AND token_expires_at > sqlc.arg(now) AND completed_at IS NULL;

-- name: AddSuperadmin :exec
INSERT INTO superadmins (user_id) VALUES (sqlc.arg(user_id));

-- name: IsSuperadmin :one
SELECT EXISTS (SELECT 1 FROM superadmins WHERE user_id = sqlc.arg(user_id)) AS superadmin;
