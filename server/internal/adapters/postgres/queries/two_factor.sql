-- Second factors (instance-level, per user; M1-03).

-- name: GetTOTP :one
SELECT secret_sealed, (confirmed_at IS NOT NULL)::boolean AS confirmed, last_step FROM user_totp WHERE user_id = sqlc.arg(user_id);

-- name: SavePendingTOTP :execrows
-- Replaces an unconfirmed secret; a confirmed one stays (0 rows).
INSERT INTO user_totp (user_id, secret_sealed) VALUES (sqlc.arg(user_id), sqlc.arg(secret_sealed))
ON CONFLICT (user_id) DO UPDATE SET secret_sealed = EXCLUDED.secret_sealed, created_at = now(), last_step = 0
WHERE user_totp.confirmed_at IS NULL;

-- name: ConfirmTOTP :execrows
UPDATE user_totp SET confirmed_at = now(), last_step = sqlc.arg(step)
WHERE user_id = sqlc.arg(user_id) AND confirmed_at IS NULL;

-- name: UseTOTPStep :execrows
UPDATE user_totp SET last_step = sqlc.arg(step)
WHERE user_id = sqlc.arg(user_id) AND confirmed_at IS NOT NULL AND last_step < sqlc.arg(step);

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_recovery_codes WHERE user_id = sqlc.arg(user_id);

-- name: InsertRecoveryCode :exec
INSERT INTO user_recovery_codes (user_id, code_hash) VALUES (sqlc.arg(user_id), sqlc.arg(code_hash));

-- name: UseRecoveryCode :execrows
UPDATE user_recovery_codes SET used_at = now()
WHERE user_id = sqlc.arg(user_id) AND code_hash = sqlc.arg(code_hash) AND used_at IS NULL;

-- name: RecoveryCodesLeft :one
SELECT count(*) FROM user_recovery_codes WHERE user_id = sqlc.arg(user_id) AND used_at IS NULL;

-- name: DeleteTOTP :exec
DELETE FROM user_totp WHERE user_id = sqlc.arg(user_id);

-- name: OrganizationRequiresSecondFactor :one
-- In the tenant transaction of the organization (RLS).
SELECT require_second_factor FROM organizations WHERE id = current_org_id();

-- name: SetOrganizationRequiresSecondFactor :execrows
UPDATE organizations SET require_second_factor = sqlc.arg(required) WHERE id = current_org_id();
