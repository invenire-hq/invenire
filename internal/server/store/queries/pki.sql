-- name: GetSigningCA :one
SELECT * FROM tenant_ca WHERE tenant_id = $1 AND retired_at IS NULL;

-- name: InsertSigningCA :one
-- Duas criações ao mesmo tempo: só uma vence; a outra relê com GetSigningCA.
INSERT INTO tenant_ca (tenant_id, cert, fingerprint, key_sealed)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id) WHERE retired_at IS NULL DO NOTHING
RETURNING *;

-- name: ImportVerifyOnlyCA :one
-- CA que só confere (sem chave), já aposentada para assinar.
INSERT INTO tenant_ca (tenant_id, cert, fingerprint, retired_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (tenant_id, fingerprint) DO UPDATE SET fingerprint = EXCLUDED.fingerprint
RETURNING *;

-- name: TenantsWithLegacyAgents :many
SELECT DISTINCT a.tenant_id FROM agent g JOIN asset a ON a.id = g.asset_id
WHERE g.tenant_ca_id IS NULL;

-- name: AssignLegacyAgents :execrows
UPDATE agent g SET tenant_ca_id = $2
FROM asset a
WHERE a.id = g.asset_id AND a.tenant_id = $1 AND g.tenant_ca_id IS NULL;

-- name: GetAgentForAuth :one
-- Autenticação mTLS: o agente pelo crachá, com a CA que o assinou. Agente
-- revogado, CA revogada ou agente sem CA não são encontrados.
SELECT sqlc.embed(g), c.cert AS ca_cert
FROM agent g
JOIN tenant_ca c ON c.id = g.tenant_ca_id
WHERE g.cert_fingerprint = $1 AND g.revoked_at IS NULL AND c.revoked_at IS NULL;

-- name: RevokeTenantCA :exec
UPDATE tenant_ca SET retired_at = coalesce(retired_at, now()), revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: GetTenant :one
SELECT * FROM tenant WHERE id = $1;
