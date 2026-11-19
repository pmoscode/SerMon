-- Users and memberships. Users are visible only through a membership in the current organization.

-- No RETURNING: a new user is invisible to the tenant until its membership exists
-- (SELECT policy), so RETURNING would be rejected by RLS. Add the membership in the same
-- transaction (AddMembership).
-- name: CreateUser :exec
INSERT INTO users (id, email, display_name)
VALUES (sqlc.arg(id), sqlc.arg(email), sqlc.arg(display_name));

-- name: AddMembership :exec
INSERT INTO memberships (org_id, user_id)
VALUES (sqlc.arg(org_id), sqlc.arg(user_id));

-- name: ListUsers :many
SELECT id, email, display_name, created_at
FROM users
ORDER BY lower(email);
