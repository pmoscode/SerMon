-- Self-monitoring of the server (M2-24). Instance level, no tenant.

-- name: SuperadminEmails :many
SELECT email::text FROM superadmin_emails() AS email;

-- name: DatabaseSize :one
SELECT pg_database_size(current_database())::bigint AS bytes;
