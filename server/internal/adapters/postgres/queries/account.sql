-- Accounts of their owners (V1-17). The rows of users are reached only through the SECURITY
-- DEFINER functions of 00075/00076 for the user the server names from the session; the tables
-- without tenant (sessions, credentials, factors) directly by user_id; the tenant tables of the
-- export inside InTenant, filtered by the user.

-- name: AccountProfile :one
SELECT email::text AS email, display_name::text AS display_name, created_at::timestamptz AS created_at
FROM account_export(sqlc.arg(user_id));

-- name: AccountSetDisplayName :one
SELECT account_set_display_name(sqlc.arg(user_id), sqlc.arg(display_name))::boolean AS changed;

-- name: AccountSetEmail :one
SELECT account_set_email(sqlc.arg(user_id), sqlc.arg(email))::boolean AS changed;

-- name: GetEmailChange :one
SELECT new_email, code_hash, attempts, expires_at FROM email_changes WHERE user_id = sqlc.arg(user_id);

-- name: UpsertEmailChange :exec
INSERT INTO email_changes (user_id, new_email, code_hash, attempts, expires_at)
VALUES (sqlc.arg(user_id), sqlc.arg(new_email), sqlc.arg(code_hash), 0, sqlc.arg(expires_at))
ON CONFLICT (user_id) DO UPDATE
SET new_email = EXCLUDED.new_email, code_hash = EXCLUDED.code_hash, attempts = 0,
    created_at = now(), expires_at = EXCLUDED.expires_at;

-- name: CountEmailChangeAttempt :one
UPDATE email_changes SET attempts = LEAST(attempts + 1, 100) WHERE user_id = sqlc.arg(user_id) RETURNING attempts;

-- name: DeleteEmailChange :exec
DELETE FROM email_changes WHERE user_id = sqlc.arg(user_id);

-- name: PurgeEmailChanges :execrows
DELETE FROM email_changes WHERE expires_at < sqlc.arg(before);

-- name: DeleteAccount :one
SELECT account_delete(sqlc.arg(user_id), sqlc.narg(actor_id))::integer AS memberships;

-- name: PurgeAccounts :many
SELECT id::uuid AS id FROM accounts_purge(sqlc.arg(before), sqlc.arg(max_rows)::integer) AS id;

-- name: PurgeExpiredInvitations :one
SELECT invitations_purge_expired(sqlc.arg(before))::bigint AS deleted;

-- name: PruneOrgAudit :many
SELECT through_seq::bigint AS through_seq, last_hash::bytea AS last_hash, deleted::bigint AS deleted
FROM audit_log_prune(sqlc.arg(org_id), sqlc.arg(before));

-- name: PruneInstanceAudit :many
SELECT through_seq::bigint AS through_seq, last_hash::bytea AS last_hash, deleted::bigint AS deleted
FROM instance_audit_log_prune(sqlc.arg(before));

-- Export: the tables of the account without tenant.

-- name: ExportPassword :many
SELECT password_changed_at FROM user_credentials WHERE user_id = sqlc.arg(user_id);

-- name: ExportTOTP :many
SELECT confirmed_at FROM user_totp WHERE user_id = sqlc.arg(user_id) AND confirmed_at IS NOT NULL;

-- name: ExportRecoveryCodesLeft :one
SELECT count(*) FROM user_recovery_codes WHERE user_id = sqlc.arg(user_id) AND used_at IS NULL;

-- name: ExportPasskeys :many
SELECT name, created_at, last_used_at FROM webauthn_credentials WHERE user_id = sqlc.arg(user_id) ORDER BY created_at;

-- name: ExportIdentities :many
SELECT issuer, subject, email, created_at, last_login_at FROM user_identities WHERE user_id = sqlc.arg(user_id) ORDER BY created_at;

-- name: ExportSessions :many
SELECT created_at, last_seen_at FROM sessions WHERE user_id = sqlc.arg(user_id) ORDER BY created_at;

-- Export: inside the tenant of each organization of the account.

-- name: ExportMembership :many
SELECT created_at FROM memberships WHERE user_id = sqlc.arg(user_id);

-- name: ExportRoles :many
SELECT COALESCE(g.name, '*')::text AS group_name, b.role, b.origin
FROM role_bindings b LEFT JOIN node_groups g ON g.id = b.group_id
WHERE b.user_id = sqlc.arg(user_id)
ORDER BY group_name, b.role;

-- name: ExportDashboards :many
SELECT title, shared, updated_at FROM dashboards WHERE owner_id = sqlc.arg(user_id) ORDER BY updated_at, id;

-- name: ExportAuditEntries :many
SELECT at, action FROM audit_log WHERE actor_id = sqlc.arg(user_id) ORDER BY seq LIMIT sqlc.arg(max_rows);
