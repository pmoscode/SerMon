-- Notification channels and deliveries (M2-10, ADR 0007). Every query runs in the
-- organization's tenant transaction.

-- name: InsertNotificationChannel :exec
INSERT INTO notification_channels (org_id, id, type, name, language, enabled, settings, secret_names, sealed_secrets,
                                   created_at, updated_at, created_by)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(type), sqlc.arg(name), sqlc.arg(language), sqlc.arg(enabled),
        sqlc.arg(settings), sqlc.arg(secret_names)::text[], sqlc.arg(sealed_secrets), sqlc.arg(created_at),
        sqlc.arg(updated_at), sqlc.narg(created_by));

-- name: UpdateNotificationChannel :execrows
UPDATE notification_channels
SET name = sqlc.arg(name), language = sqlc.arg(language), enabled = sqlc.arg(enabled), settings = sqlc.arg(settings),
    secret_names = sqlc.arg(secret_names)::text[], sealed_secrets = sqlc.arg(sealed_secrets), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE id = sqlc.arg(id);

-- name: ListNotificationChannels :many
SELECT id, type, name, language, enabled, settings, secret_names, sealed_secrets, created_at, updated_at, created_by
FROM notification_channels
ORDER BY created_at, id;

-- name: GetNotificationChannel :one
SELECT id, type, name, language, enabled, settings, secret_names, sealed_secrets, created_at, updated_at, created_by
FROM notification_channels
WHERE id = sqlc.arg(id);

-- name: InsertNotificationDelivery :batchexec
INSERT INTO notification_deliveries (org_id, channel_id, notification, test, status, attempts, created_at,
                                     next_attempt_at, last_attempt_at, sent_at, last_error)
VALUES (current_org_id(), sqlc.arg(channel_id), sqlc.arg(notification), sqlc.arg(test), sqlc.arg(status),
        sqlc.arg(attempts), sqlc.arg(created_at), sqlc.arg(next_attempt_at), sqlc.narg(last_attempt_at),
        sqlc.narg(sent_at), sqlc.arg(last_error));

-- name: ClaimNotificationDeliveries :many
-- Due rows: pending ones whose attempt is due, and sending ones whose claim expired. SKIP LOCKED
-- lets concurrent instances claim different rows.
UPDATE notification_deliveries d
SET status = 'sending', claimed_until = sqlc.arg(until), attempts = d.attempts + 1
WHERE (d.org_id, d.id) IN (
    SELECT q.org_id, q.id FROM notification_deliveries q
    WHERE (q.status = 'pending' AND q.next_attempt_at <= sqlc.arg(now))
       OR (q.status = 'sending' AND q.claimed_until < sqlc.arg(now))
    ORDER BY q.next_attempt_at, q.id
    LIMIT sqlc.arg(max_rows)
    FOR UPDATE SKIP LOCKED)
RETURNING d.id, d.channel_id, d.notification, d.test, d.status, d.attempts, d.created_at, d.next_attempt_at,
          d.claimed_until, d.last_attempt_at, d.sent_at, d.last_error;

-- name: FinishNotificationDelivery :exec
UPDATE notification_deliveries
SET status = sqlc.arg(status), claimed_until = NULL, next_attempt_at = sqlc.arg(next_attempt_at),
    last_attempt_at = sqlc.narg(last_attempt_at), sent_at = sqlc.narg(sent_at), last_error = sqlc.arg(last_error)
WHERE id = sqlc.arg(id);

-- name: ListNotificationDeliveries :many
SELECT id, channel_id, notification, test, status, attempts, created_at, next_attempt_at, claimed_until,
       last_attempt_at, sent_at, last_error
FROM notification_deliveries
WHERE (sqlc.narg(channel_id)::uuid IS NULL OR channel_id = sqlc.narg(channel_id)::uuid)
  AND (sqlc.arg(before)::bigint = 0 OR id < sqlc.arg(before)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(max_rows);

-- name: PurgeNotificationDeliveries :execrows
DELETE FROM notification_deliveries
WHERE created_at < sqlc.arg(before) AND status IN ('sent', 'failed');
