-- Organizations. Row-level security limits every query to the organization of the tenant
-- transaction (postgres.DB.InTenant); there is no way to read other organizations from here.

-- name: CreateOrganization :one
INSERT INTO organizations (id, name)
VALUES (sqlc.arg(id), sqlc.arg(name))
RETURNING id, name, created_at;

-- name: GetOrganization :one
SELECT id, name, created_at
FROM organizations
WHERE id = sqlc.arg(id);

-- name: ListOrganizations :many
SELECT id, name, created_at
FROM organizations
ORDER BY name;

-- Superadmin view of all organizations (SECURITY DEFINER; the caller is checked in the app layer).
-- name: AdminListOrganizations :many
SELECT id::uuid AS id, name::text AS name, created_at::timestamptz AS created_at, members::bigint AS members
FROM admin_list_organizations();

-- name: RenameOrganization :execrows
UPDATE organizations SET name = sqlc.arg(name) WHERE id = current_org_id();
