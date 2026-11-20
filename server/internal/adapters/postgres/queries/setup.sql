-- First installation: setup token and superadmins (instance-level tables, no RLS).

-- name: SetupCompleted :one
SELECT EXISTS (SELECT 1 FROM instance_setup WHERE completed_at IS NOT NULL) AS completed;

-- name: IssueSetupToken :one
-- Replaces a previous token; false once the setup is completed (definer function, M1-40).
SELECT issue_setup_token(sqlc.arg(token_hash), sqlc.arg(expires_at))::boolean AS issued;

-- name: CompleteInstanceSetup :one
-- Claims the token and adds the superadmin; false without a valid token (definer function).
SELECT complete_instance_setup(sqlc.arg(token_hash), sqlc.arg(user_id), sqlc.arg(now))::boolean AS completed;

-- name: IsSuperadmin :one
SELECT EXISTS (SELECT 1 FROM superadmins WHERE user_id = sqlc.arg(user_id)) AS superadmin;

-- name: SetupTokenValid :one
-- Read-only pre-check (sermon_app may only read instance_setup); CompleteInstanceSetup consumes.
SELECT EXISTS (
    SELECT 1 FROM instance_setup
    WHERE token_hash = sqlc.arg(token_hash) AND token_expires_at > sqlc.arg(now) AND completed_at IS NULL
) AS valid;
