-- Agent PKI. pki_authorities is instance-level; agent_certificates is tenant-scoped (RLS).

-- name: TrustedAuthorities :many
SELECT id, root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed, active, created_at
FROM pki_authorities
WHERE retired_at IS NULL
ORDER BY id;

-- name: AllAuthorities :many
SELECT id, root_cert, active, created_at, retired_at,
       pki_certificates_in_use(id, sqlc.arg(at))::bigint AS in_use
FROM pki_authorities
ORDER BY id;

-- Serializes rollover operations of all server instances.
-- name: LockPKI :exec
SELECT pg_advisory_xact_lock(hashtextextended('sermon:pki', 0));

-- name: CountTrustedAuthorities :one
SELECT count(*) FROM pki_authorities WHERE retired_at IS NULL;

-- name: DeactivateIssuingAuthority :exec
UPDATE pki_authorities SET active = false WHERE active;

-- name: InsertIssuingAuthority :one
INSERT INTO pki_authorities (root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed, started_by)
VALUES (sqlc.arg(root_cert), sqlc.arg(root_key_sealed), sqlc.arg(intermediate_cert), sqlc.arg(intermediate_key_sealed), sqlc.narg(started_by))
RETURNING id;

-- name: AuthorityForRetirement :one
SELECT active, root_cert, pki_certificates_in_use(id, sqlc.arg(at))::bigint AS in_use
FROM pki_authorities WHERE id = sqlc.arg(id) AND retired_at IS NULL;

-- name: RetireAuthority :exec
UPDATE pki_authorities SET retired_at = sqlc.arg(at), retired_by = sqlc.narg(retired_by) WHERE id = sqlc.arg(id);

-- name: InsertAuthority :one
INSERT INTO pki_authorities (root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed)
VALUES (sqlc.arg(root_cert), sqlc.arg(root_key_sealed), sqlc.arg(intermediate_cert), sqlc.arg(intermediate_key_sealed))
RETURNING id;

-- name: InsertAgentCertificate :exec
INSERT INTO agent_certificates (serial, org_id, node_id, authority_id, not_before, not_after)
VALUES (sqlc.arg(serial), sqlc.arg(org_id), sqlc.arg(node_id), sqlc.arg(authority_id), sqlc.arg(not_before), sqlc.arg(not_after));

-- name: RevokeAgentCertificate :execrows
UPDATE agent_certificates
SET revoked_at = sqlc.arg(revoked_at)
WHERE serial = sqlc.arg(serial) AND revoked_at IS NULL;

-- name: AgentCertificateUsable :one
SELECT agent_certificate_usable(sqlc.arg(serial), sqlc.arg(org_id)::uuid, sqlc.arg(node_id)::uuid, sqlc.arg(at))::boolean AS usable;
