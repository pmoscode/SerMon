-- Invitations (ADR 0008). Management runs inside InTenant; the lookup by token uses the
-- SECURITY DEFINER function invitation_by_token (no tenant known yet).

-- name: UpsertInvitation :exec
INSERT INTO invitations (id, org_id, email, group_id, role, token_hash, invited_by, expires_at)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(email), sqlc.narg(group_id), sqlc.arg(role),
        sqlc.arg(token_hash), sqlc.narg(invited_by), sqlc.arg(expires_at))
ON CONFLICT (org_id, email) DO UPDATE
SET id = EXCLUDED.id, group_id = EXCLUDED.group_id, role = EXCLUDED.role, token_hash = EXCLUDED.token_hash,
    invited_by = EXCLUDED.invited_by, created_at = now(), expires_at = EXCLUDED.expires_at;

-- name: ListInvitations :many
SELECT id, email, group_id, role, invited_by, created_at, expires_at
FROM invitations ORDER BY created_at DESC, id;

-- name: DeleteInvitation :execrows
DELETE FROM invitations WHERE id = sqlc.arg(id);

-- name: InvitationByToken :one
SELECT id::uuid AS id, org_id::uuid AS org_id, org_name::text AS org_name, email::text AS email,
       -- nil UUID = all groups (sqlc cannot type nullable columns of function results)
       COALESCE(group_id, '00000000-0000-0000-0000-000000000000')::uuid AS group_id,
       role::text AS role, expires_at::timestamptz AS expires_at
FROM invitation_by_token(sqlc.arg(token_hash), sqlc.arg(at));

-- name: AddMembershipIfMissing :exec
INSERT INTO memberships (org_id, user_id) VALUES (current_org_id(), sqlc.arg(user_id))
ON CONFLICT DO NOTHING;

-- Accepting an invitation never lowers an existing role on the same group (viewer < operator < admin).
-- The binding counts as given by hand afterwards (M3-21): sign-ins over OIDC leave it.
-- name: GrantRoleKeepHigher :exec
INSERT INTO role_bindings (id, org_id, user_id, group_id, role)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(user_id), sqlc.narg(group_id), sqlc.arg(role))
ON CONFLICT (org_id, user_id, COALESCE(group_id, '00000000-0000-0000-0000-000000000000'::uuid))
DO UPDATE SET role = CASE
    WHEN array_position(ARRAY['viewer', 'operator', 'admin'], EXCLUDED.role)
       > array_position(ARRAY['viewer', 'operator', 'admin'], role_bindings.role)
    THEN EXCLUDED.role ELSE role_bindings.role END,
    origin = 'manual';
