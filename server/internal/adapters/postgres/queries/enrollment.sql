-- Enrollment tokens (M1-08). Management runs inside InTenant; redemption looks the token up by
-- hash through the SECURITY DEFINER function and consumes it inside the token's tenant.

-- name: InsertEnrollmentToken :exec
INSERT INTO enrollment_tokens (id, org_id, group_id, token_hash, created_by, created_at, expires_at, max_uses)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(group_id), sqlc.arg(token_hash), sqlc.narg(created_by),
        sqlc.arg(created_at), sqlc.arg(expires_at), sqlc.arg(max_uses));

-- Open tokens (not revoked, not expired, not used up) of the given groups.
-- name: ListEnrollmentTokens :many
SELECT id, group_id, created_by, created_at, expires_at, max_uses, uses
FROM enrollment_tokens
WHERE revoked_at IS NULL AND expires_at > sqlc.arg(now) AND uses < max_uses
  AND (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]))
ORDER BY created_at DESC, id;

-- name: RevokeEnrollmentToken :execrows
UPDATE enrollment_tokens SET revoked_at = now()
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
  AND (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]));

-- name: EnrollmentTokenByHash :one
SELECT id::uuid AS id, org_id::uuid AS org_id, group_id::uuid AS group_id, token_hash::bytea AS token_hash
FROM enrollment_token_by_hash(sqlc.arg(token_hash), sqlc.arg(at));

-- Atomic: concurrent redemptions cannot exceed max_uses.
-- name: ConsumeEnrollmentToken :execrows
UPDATE enrollment_tokens SET uses = uses + 1
WHERE id = sqlc.arg(id) AND revoked_at IS NULL AND expires_at > sqlc.arg(now) AND uses < max_uses;
