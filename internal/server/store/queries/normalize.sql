-- Normalização, reaplicação de mapeamentos e a lista de não identificados.

-- name: PruneAssetRaws :exec
-- Tira do ativo os nomes que o último snapshot não trouxe.
DELETE FROM asset_software_raw
WHERE asset_id = $1 AND NOT (software_raw_id = ANY(sqlc.arg(raw_ids)::uuid[]));

-- name: AddAssetRaws :exec
INSERT INTO asset_software_raw (asset_id, software_raw_id)
SELECT $1, unnest(sqlc.arg(raw_ids)::uuid[])
ON CONFLICT DO NOTHING;

-- name: MarkAssetsForRemap :execrows
-- Os ativos que têm o nome bruto precisam recalcular as instalações.
UPDATE asset SET remap_pending = true
WHERE id IN (SELECT asset_id FROM asset_software_raw WHERE software_raw_id = $1);

-- name: ClearRemap :exec
UPDATE asset SET remap_pending = false WHERE id = $1;

-- name: ClaimRemapAsset :one
SELECT * FROM asset WHERE remap_pending
ORDER BY id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: LatestAppliedSnapshot :one
-- O snapshot que define o estado atual do ativo.
SELECT id, collected_at, payload FROM snapshot
WHERE asset_id = $1 AND processed_at IS NOT NULL AND ingest_error IS NULL
ORDER BY collected_at DESC, received_at DESC
LIMIT 1;

-- name: FindSoftware :one
-- Procura o produto sem criar, para não escrever quando nada mudou.
SELECT s.id FROM software s
LEFT JOIN vendor v ON v.id = s.vendor_id
WHERE s.product = sqlc.arg(product) AND v.name IS NOT DISTINCT FROM sqlc.narg(vendor);

-- name: UnmapSoftwareRaw :exec
UPDATE software_raw SET software_id = NULL, confidence = NULL, mapped_by = NULL,
  mapped_rule = NULL, mapped_at = NULL
WHERE id = $1;

-- name: ListRuleCandidates :many
-- Nomes que as regras podem mudar: os sem produto e os mapeados por regra.
-- Mapeamento manual (mapped_rule nulo com produto) nunca é tocado por regra.
SELECT id, source, raw_name, raw_vendor, software_id, mapped_rule FROM software_raw
WHERE (software_id IS NULL OR mapped_rule IS NOT NULL) AND id > $1
ORDER BY id
LIMIT $2;

-- name: GetSoftwareRawForUpdate :one
SELECT * FROM software_raw WHERE id = $1 FOR UPDATE;

-- name: RawTenants :many
-- Em quais tenants o nome bruto aparece, para o change_log de cada um.
SELECT DISTINCT a.tenant_id FROM asset_software_raw ar
JOIN asset a ON a.id = ar.asset_id
WHERE ar.software_raw_id = $1;

-- name: ListUnmappedForTenant :many
-- A lista de software não identificado de um tenant, dos nomes em mais
-- ativos para os em menos. Cursor: (asset_count, id) do último item.
WITH u AS (
  SELECT r.id, r.source, r.raw_name, r.raw_vendor, r.first_seen_at, count(*)::int AS asset_count
  FROM software_raw r
  JOIN asset_software_raw ar ON ar.software_raw_id = r.id
  JOIN asset a ON a.id = ar.asset_id
  WHERE r.software_id IS NULL AND a.tenant_id = sqlc.arg(tenant_id)
    AND (sqlc.narg(source)::text IS NULL OR r.source = sqlc.narg(source))
  GROUP BY r.id
)
SELECT * FROM u
WHERE sqlc.narg(after_count)::int IS NULL
   OR (u.asset_count, u.id) < (sqlc.narg(after_count)::int, sqlc.narg(after_id)::uuid)
ORDER BY u.asset_count DESC, u.id DESC
LIMIT sqlc.arg(page_size);

-- name: CreateAPIToken :one
INSERT INTO api_token (actor_id, token_hash, description, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: GetActorByAPIToken :one
-- Autenticação de usuários e pipelines: token válido de ator ativo.
SELECT a.* FROM api_token t
JOIN actor a ON a.id = t.actor_id
WHERE t.token_hash = $1 AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > now())
  AND a.disabled_at IS NULL;

-- name: GetTenantActor :one
SELECT * FROM actor WHERE tenant_id = $1 AND kind = $2 AND name = $3;
