-- Identidade de software e instalações.
--
-- Os "Ensure" são get-or-create: o DO UPDATE sem efeito existe só para o
-- RETURNING devolver a linha que já estava lá.

-- name: EnsureVendor :one
-- tenant_id nulo = catálogo comum.
INSERT INTO vendor (tenant_id, name) VALUES (sqlc.narg(tenant_id), sqlc.arg(name))
ON CONFLICT (tenant_id, name) DO UPDATE SET name = EXCLUDED.name
RETURNING *;

-- name: FindVendor :one
-- O fabricante do catálogo comum vence o do tenant com o mesmo nome.
SELECT id FROM vendor
WHERE name = sqlc.arg(name) AND (tenant_id IS NULL OR tenant_id = sqlc.narg(tenant_id))
ORDER BY tenant_id NULLS FIRST
LIMIT 1;

-- name: EnsureSoftware :one
INSERT INTO software (tenant_id, vendor_id, product) VALUES (sqlc.narg(tenant_id), sqlc.narg(vendor_id), sqlc.arg(product))
ON CONFLICT (tenant_id, vendor_id, product) DO UPDATE SET product = EXCLUDED.product
RETURNING *;

-- name: EnsureSoftwareVersion :one
INSERT INTO software_version (software_id, version, purl) VALUES ($1, $2, $3)
ON CONFLICT (software_id, version) DO UPDATE SET purl = COALESCE(software_version.purl, EXCLUDED.purl)
RETURNING *;

-- name: EnsureSoftwareRaw :one
INSERT INTO software_raw (source, raw_name, raw_vendor) VALUES ($1, $2, $3)
ON CONFLICT (source, raw_name, raw_vendor) DO UPDATE SET source = EXCLUDED.source
RETURNING *;

-- name: MapSoftwareRaw :exec
-- Liga o nome bruto a um produto. mapped_rule vazio = mapeamento manual ou
-- proposta aprovada.
UPDATE software_raw SET
  software_id = $2,
  confidence  = $3,
  mapped_by   = $4,
  mapped_rule = sqlc.narg(mapped_rule),
  mapped_at   = now()
WHERE id = $1;

-- name: ListUnmappedSoftware :many
-- A lista de software não identificado, paginada por first_seen_at. Nome sem
-- produto não vira installation; em quais ativos ele aparece sai dos snapshots.
SELECT r.id, r.source, r.raw_name, r.raw_vendor, r.first_seen_at
FROM software_raw r
WHERE r.software_id IS NULL
  AND (sqlc.narg(source)::text IS NULL OR r.source = sqlc.narg(source))
  AND r.first_seen_at > sqlc.arg(after)
ORDER BY r.first_seen_at, r.id
LIMIT sqlc.arg(page_size);

-- name: ListOpenInstallations :many
SELECT id, software_version_id, install_path FROM installation
WHERE asset_id = $1 AND removed_at IS NULL;

-- name: OpenInstallation :one
INSERT INTO installation (asset_id, software_version_id, install_path, first_snapshot_id, last_snapshot_id)
VALUES ($1, $2, $3, $4, $4)
RETURNING id;

-- name: TouchInstallations :exec
UPDATE installation SET last_snapshot_id = $1
WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND removed_at IS NULL;

-- name: CloseInstallations :exec
UPDATE installation SET removed_at = now(), removed_snapshot_id = $1
WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND removed_at IS NULL;

-- name: FindSoftwareVersionInstallations :many
-- Em quais ativos uma versão está instalada agora (find_software do MCP).
SELECT a.id AS asset_id, a.hostname, i.install_path, s.collected_at AS last_seen_in
FROM installation i
JOIN asset a    ON a.id = i.asset_id
JOIN snapshot s ON s.id = i.last_snapshot_id
WHERE i.software_version_id = $1 AND i.removed_at IS NULL AND a.tenant_id = $2
ORDER BY a.hostname;
