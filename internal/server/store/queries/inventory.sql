-- Tenant, ativo, agente e snapshot: enrollment e recepção de snapshots.

-- name: CreateTenant :one
INSERT INTO tenant (name) VALUES ($1) RETURNING *;

-- name: GetTenantByName :one
SELECT * FROM tenant WHERE name = $1;

-- name: UpsertAsset :one
-- Cria o ativo na primeira vez que aparece e atualiza os atributos depois.
-- inserted diz se é novo, para registrar asset_new no change_log.
INSERT INTO asset (tenant_id, fingerprint, hostname, serial, manufacturer, model,
                   os_family, os_name, os_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, fingerprint) DO UPDATE SET
  hostname     = EXCLUDED.hostname,
  serial       = EXCLUDED.serial,
  manufacturer = EXCLUDED.manufacturer,
  model        = EXCLUDED.model,
  os_family    = EXCLUDED.os_family,
  os_name      = EXCLUDED.os_name,
  os_version   = EXCLUDED.os_version,
  last_seen_at = now()
RETURNING *, (xmax = 0)::boolean AS inserted;

-- name: GetAsset :one
SELECT * FROM asset WHERE id = $1;

-- name: CreateAgent :one
INSERT INTO agent (asset_id, cert_fingerprint, agent_version)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetActiveAgentByCert :one
-- Usado na autenticação mTLS: agente revogado não é encontrado.
SELECT * FROM agent WHERE cert_fingerprint = $1 AND revoked_at IS NULL;

-- name: TouchAgent :exec
UPDATE agent SET last_seen_at = now(), agent_version = $2 WHERE id = $1;

-- name: RevokeAgent :exec
UPDATE agent SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: InsertSnapshot :one
-- O SHA-256 é único: reenviar o mesmo snapshot devolve o existente.
INSERT INTO snapshot (asset_id, agent_id, schema_version, collected_at, sha256, prev_sha256, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (sha256) DO UPDATE SET sha256 = EXCLUDED.sha256
RETURNING id, (xmax = 0)::boolean AS inserted;

-- name: GetLastSnapshotHash :one
-- Para conferir o prev_sha256 do snapshot que está chegando.
SELECT sha256 FROM snapshot
WHERE asset_id = $1
ORDER BY collected_at DESC, received_at DESC
LIMIT 1;

-- name: ClaimUnprocessedSnapshots :many
-- Pega snapshots para a ingestão. SKIP LOCKED permite vários workers.
SELECT id, asset_id, payload FROM snapshot
WHERE processed_at IS NULL
ORDER BY received_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkSnapshotProcessed :exec
UPDATE snapshot SET processed_at = now() WHERE id = $1;
