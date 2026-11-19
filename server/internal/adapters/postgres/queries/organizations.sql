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
