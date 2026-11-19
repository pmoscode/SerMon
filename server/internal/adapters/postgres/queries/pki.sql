-- Agent PKI. pki_authorities is instance-level; agent_certificates is tenant-scoped (RLS).

-- name: ActiveAuthority :one
SELECT id, root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed
FROM pki_authorities
WHERE active;

-- name: InsertAuthority :one
INSERT INTO pki_authorities (root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed)
VALUES (sqlc.arg(root_cert), sqlc.arg(root_key_sealed), sqlc.arg(intermediate_cert), sqlc.arg(intermediate_key_sealed))
RETURNING id;

-- name: InsertAgentCertificate :exec
INSERT INTO agent_certificates (serial, org_id, node_id, not_before, not_after)
VALUES (sqlc.arg(serial), sqlc.arg(org_id), sqlc.arg(node_id), sqlc.arg(not_before), sqlc.arg(not_after));

-- name: RevokeAgentCertificate :execrows
UPDATE agent_certificates
SET revoked_at = sqlc.arg(revoked_at)
WHERE serial = sqlc.arg(serial) AND revoked_at IS NULL;

-- name: AgentCertificateUsable :one
SELECT agent_certificate_usable(sqlc.arg(serial), sqlc.arg(at))::boolean AS usable;
