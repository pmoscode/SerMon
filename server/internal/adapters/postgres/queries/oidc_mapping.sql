-- OIDC group mappings (M3-21). All but OIDCMappedOrganizations run inside InTenant.

-- name: ListOIDCMappings :many
SELECT id, oidc_group, group_id, role, created_at FROM oidc_group_mappings ORDER BY lower(oidc_group), created_at, id;

-- name: CountOIDCMappings :one
SELECT count(*) FROM oidc_group_mappings;

-- name: CreateOIDCMapping :exec
INSERT INTO oidc_group_mappings (id, org_id, oidc_group, group_id, role, created_at)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(oidc_group), sqlc.narg(group_id), sqlc.arg(role), sqlc.arg(created_at));

-- name: DeleteOIDCMapping :one
DELETE FROM oidc_group_mappings WHERE id = sqlc.arg(id) RETURNING oidc_group, group_id, role;

-- name: OIDCMappedOrganizations :many
SELECT org_id::uuid AS org_id FROM oidc_mapped_organizations(sqlc.arg(groups)::text[]);

-- name: OrganizationLocked :one
SELECT (locked_at IS NOT NULL)::boolean AS locked FROM organizations WHERE id = current_org_id();

-- name: RoleBindingsWithOrigin :many
SELECT id, group_id, role, origin FROM role_bindings WHERE user_id = sqlc.arg(user_id);

-- name: CreateOIDCRoleBinding :exec
INSERT INTO role_bindings (id, org_id, user_id, group_id, role, origin)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(user_id), sqlc.narg(group_id), sqlc.arg(role), 'oidc');

-- name: UpdateOIDCRoleBinding :execrows
UPDATE role_bindings SET role = sqlc.arg(role) WHERE id = sqlc.arg(id) AND origin = 'oidc';

-- name: DeleteOIDCRoleBinding :execrows
DELETE FROM role_bindings WHERE id = sqlc.arg(id) AND origin = 'oidc';

-- name: AddMembershipOfCurrentOrg :exec
INSERT INTO memberships (org_id, user_id) VALUES (current_org_id(), sqlc.arg(user_id));
