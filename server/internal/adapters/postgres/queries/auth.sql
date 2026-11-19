-- Authentication (instance-level). Tenant tables are only reached through SECURITY DEFINER checks.

-- name: FindUserByEmail :one
SELECT COALESCE(auth_find_user(sqlc.arg(email)), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS user_id;

-- name: UserOrganizations :many
SELECT org_id::uuid AS org_id, name::text AS name FROM auth_user_organizations(sqlc.arg(user_id));

-- name: GetCredentials :one
SELECT user_id, password_hash, failed_attempts, locked_until
FROM user_credentials
WHERE user_id = sqlc.arg(user_id);

-- name: SetPassword :exec
INSERT INTO user_credentials (user_id, password_hash, password_changed_at)
VALUES (sqlc.arg(user_id), sqlc.arg(password_hash), sqlc.arg(changed_at))
ON CONFLICT (user_id) DO UPDATE
SET password_hash = EXCLUDED.password_hash, password_changed_at = EXCLUDED.password_changed_at,
    failed_attempts = 0, locked_until = NULL;

-- name: UpdatePasswordHash :exec
UPDATE user_credentials SET password_hash = sqlc.arg(password_hash) WHERE user_id = sqlc.arg(user_id);

-- name: RecordFailedLogin :exec
UPDATE user_credentials
SET failed_attempts = failed_attempts + 1, locked_until = sqlc.narg(locked_until)
WHERE user_id = sqlc.arg(user_id);

-- name: ResetFailedLogins :exec
UPDATE user_credentials SET failed_attempts = 0, locked_until = NULL WHERE user_id = sqlc.arg(user_id);

-- name: InsertSession :exec
INSERT INTO sessions (id, user_id, org_id, created_at, last_seen_at, step_up_at)
VALUES (sqlc.arg(id), sqlc.arg(user_id), sqlc.narg(org_id), sqlc.arg(created_at), sqlc.arg(last_seen_at), sqlc.arg(step_up_at));

-- name: GetSession :one
SELECT id, user_id, org_id, created_at, last_seen_at, step_up_at FROM sessions WHERE id = sqlc.arg(id);

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = sqlc.arg(at) WHERE id = sqlc.arg(id);

-- name: SetSessionStepUp :exec
UPDATE sessions SET step_up_at = sqlc.arg(at) WHERE id = sqlc.arg(id);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = sqlc.arg(id);

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = sqlc.arg(user_id);
