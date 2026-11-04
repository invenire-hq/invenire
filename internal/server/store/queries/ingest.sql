-- Ingestão: do snapshot imutável para asset, software_raw e installation.

-- name: ClaimNextSnapshot :one
-- Um snapshot pendente por vez, o de coleta mais antiga, travado até o fim da
-- transação. SKIP LOCKED deixa outro worker pegar o seguinte.
SELECT id, asset_id, collected_at, payload FROM snapshot
WHERE processed_at IS NULL
ORDER BY collected_at, received_at
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: LockAsset :one
-- Serializa a ingestão de um mesmo ativo: o diff depende das instalações
-- abertas, que o snapshot anterior acabou de mudar.
SELECT * FROM asset WHERE id = $1 FOR UPDATE;

-- name: LastIngestedCollectedAt :one
-- A coleta mais recente já aplicada ao ativo. Snapshot mais antigo que ela
-- chegou fora de ordem e não pode mais mudar o estado atual.
SELECT collected_at FROM snapshot
WHERE asset_id = $1 AND processed_at IS NOT NULL AND ingest_error IS NULL
ORDER BY collected_at DESC
LIMIT 1;

-- name: FinishSnapshot :exec
UPDATE snapshot SET processed_at = now(), ingest_error = sqlc.narg(ingest_error)
WHERE id = $1;

-- name: UpdateAssetFromSnapshot :exec
UPDATE asset SET
  hostname     = $2,
  serial       = $3,
  manufacturer = $4,
  model        = $5,
  os_family    = $6,
  os_name      = $7,
  os_version   = $8,
  last_seen_at = GREATEST(last_seen_at, sqlc.arg(collected_at)::timestamptz)
WHERE id = $1;

-- name: InsertSoftwareRaws :exec
-- Em lote e sem DO UPDATE: nomes já conhecidos não geram escrita.
INSERT INTO software_raw (source, raw_name, raw_vendor)
SELECT unnest(sqlc.arg(sources)::text[]), unnest(sqlc.arg(names)::text[]), unnest(sqlc.arg(vendors)::text[])
ON CONFLICT (source, raw_name, raw_vendor) DO NOTHING;

-- name: GetSoftwareRaws :many
SELECT r.id, r.source, r.raw_name, r.raw_vendor, r.software_id
FROM software_raw r
JOIN (SELECT unnest(sqlc.arg(sources)::text[]) AS source,
             unnest(sqlc.arg(names)::text[])   AS raw_name,
             unnest(sqlc.arg(vendors)::text[]) AS raw_vendor) k
  USING (source, raw_name, raw_vendor);

-- name: InsertSoftwareVersions :exec
INSERT INTO software_version (software_id, version)
SELECT unnest(sqlc.arg(software_ids)::uuid[]), unnest(sqlc.arg(versions)::text[])
ON CONFLICT (software_id, version) DO NOTHING;

-- name: GetSoftwareVersions :many
SELECT v.id, v.software_id, v.version
FROM software_version v
JOIN (SELECT unnest(sqlc.arg(software_ids)::uuid[]) AS software_id,
             unnest(sqlc.arg(versions)::text[])       AS version) k
  USING (software_id, version);

-- name: ListInstallationsWithSoftware :many
-- Para o histórico e os testes: o que está instalado num ativo, por nome.
SELECT i.id, i.install_path, i.removed_at, v.version, s.product, vd.name AS vendor
FROM installation i
JOIN software_version v ON v.id = i.software_version_id
JOIN software s         ON s.id = v.software_id
LEFT JOIN vendor vd     ON vd.id = s.vendor_id
WHERE i.asset_id = $1
ORDER BY s.product, v.version, i.install_path, i.first_snapshot_id;
