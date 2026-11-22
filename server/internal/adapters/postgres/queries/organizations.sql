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
-- Quotas: 0 is the instance default.
-- name: AdminListOrganizations :many
SELECT id::uuid AS id, name::text AS name, created_at::timestamptz AS created_at, members::bigint AS members,
       coalesce(max_nodes, 0)::integer AS max_nodes, coalesce(max_members, 0)::integer AS max_members,
       coalesce(retention_cap_days, 0)::integer AS retention_cap_days,
       coalesce(samples_per_second, 0)::integer AS samples_per_second,
       coalesce(series_per_node, 0)::integer AS series_per_node
FROM admin_organizations();

-- Storage of the instance for the estimate per organization (SECURITY DEFINER, sizes only).
-- name: AdminStorageTotals :one
SELECT metric_bytes::bigint AS metric_bytes, event_bytes::bigint AS event_bytes FROM admin_storage_totals();

-- What the organization of the tenant transaction holds, for the superadmin's estimate (M2-32).
-- name: OrganizationUsage :one
SELECT (SELECT count(*) FROM nodes)::bigint AS nodes, (SELECT count(*) FROM series)::bigint AS series,
       (SELECT count(*) FROM security_events)::bigint AS events;

-- name: UpdateOrganization :execrows
UPDATE organizations
SET name = sqlc.arg(name), max_nodes = sqlc.narg(max_nodes), max_members = sqlc.narg(max_members),
    retention_cap_days = sqlc.narg(retention_cap_days), samples_per_second = sqlc.narg(samples_per_second),
    series_per_node = sqlc.narg(series_per_node)
WHERE id = current_org_id();

-- The quotas of the organization, locked: enrollments and invitations of one organization are
-- counted one after the other.
-- name: OrganizationQuotasForUpdate :one
SELECT max_nodes, max_members FROM organizations WHERE id = current_org_id() FOR UPDATE;

-- name: OrganizationQuotas :one
SELECT max_nodes, max_members, retention_cap_days, samples_per_second, series_per_node
FROM organizations WHERE id = current_org_id();

-- name: CountNodes :one
SELECT count(*)::bigint FROM nodes;

-- name: CountMembers :one
SELECT count(*)::bigint FROM memberships;

-- Open invitations for other addresses than email (a new invitation replaces the old one).
-- name: CountOpenInvitations :one
SELECT count(*)::bigint FROM invitations WHERE expires_at > sqlc.arg(now) AND email <> sqlc.arg(email);

-- name: RenameOrganization :execrows
UPDATE organizations SET name = sqlc.arg(name) WHERE id = current_org_id();
