-- Rotas de consulta: ativos, software instalado, histórico, instalações de
-- um produto e o fluxo de mudanças. Tudo restrito ao tenant de quem pergunta.
--
-- Filtro por unidade (unit_mode): '' = todas, 'none' = ativos sem unidade,
-- 'in' = ativos nas unidades de unit_ids (a subárvore já vem expandida).
-- Paginação por chave: (after_key, after_id) é o último item da página
-- anterior, na mesma ordem do ORDER BY.

-- name: ListAssets :many
SELECT a.id, a.kind, a.hostname, a.serial, a.manufacturer, a.model,
       a.os_family, a.os_name, a.os_version, a.unit_id, a.first_seen_at, a.last_seen_at,
       (SELECT count(*) FROM installation i WHERE i.asset_id = a.id AND i.removed_at IS NULL)::int AS installed_count
FROM asset a
WHERE a.tenant_id = @tenant_id
  AND (sqlc.narg(asset_id)::uuid IS NULL OR a.id = sqlc.narg(asset_id)::uuid)
  AND (@unit_mode::text = ''
       OR (@unit_mode::text = 'none' AND a.unit_id IS NULL)
       OR (@unit_mode::text = 'in' AND a.unit_id = ANY(@unit_ids::uuid[])))
  AND (sqlc.narg(hostname)::text IS NULL OR a.hostname ILIKE sqlc.narg(hostname)::text ESCAPE '\')
  AND (sqlc.narg(os_family)::text IS NULL OR a.os_family = sqlc.narg(os_family)::text)
  AND (coalesce(cardinality(@labels::text[]), 0) = 0
       OR @labels::text[] <@ ARRAY(SELECT l.key || '=' || l.value FROM asset_label l WHERE l.asset_id = a.id))
  AND (sqlc.narg(software_id)::uuid IS NULL OR EXISTS (
         SELECT 1 FROM installation i JOIN software_version v ON v.id = i.software_version_id
         WHERE i.asset_id = a.id AND i.removed_at IS NULL AND v.software_id = sqlc.narg(software_id)::uuid))
  AND (sqlc.narg(after_key)::text IS NULL
       OR (lower(coalesce(a.hostname, '')), a.id) > (sqlc.narg(after_key)::text, sqlc.narg(after_id)::uuid))
ORDER BY lower(coalesce(a.hostname, '')), a.id
LIMIT @page_size;

-- name: ActiveAgentsOf :many
-- O agente ativo de cada ativo (o cadastro novo revoga os anteriores).
SELECT DISTINCT ON (asset_id) asset_id, id, agent_version, enrolled_at, last_seen_at
FROM agent
WHERE asset_id = ANY(@asset_ids::uuid[]) AND revoked_at IS NULL
ORDER BY asset_id, enrolled_at DESC;

-- name: LabelsOf :many
SELECT asset_id, key, value FROM asset_label
WHERE asset_id = ANY(@asset_ids::uuid[])
ORDER BY asset_id, key;

-- name: ListAssetSoftware :many
-- O que está instalado agora num ativo, em ordem de produto.
SELECT i.id, i.install_path, v.id AS version_id, v.version, v.purl,
       s.id AS software_id, s.product, s.tenant_id AS software_tenant_id, vd.name AS vendor,
       fs.collected_at AS installed_since, ls.collected_at AS last_seen_at
FROM installation i
JOIN software_version v ON v.id = i.software_version_id
JOIN software s ON s.id = v.software_id
LEFT JOIN vendor vd ON vd.id = s.vendor_id
JOIN snapshot fs ON fs.id = i.first_snapshot_id
JOIN snapshot ls ON ls.id = i.last_snapshot_id
WHERE i.asset_id = @asset_id AND i.removed_at IS NULL
  AND (sqlc.narg(product)::text IS NULL OR s.product ILIKE sqlc.narg(product)::text ESCAPE '\'
       OR vd.name ILIKE sqlc.narg(product)::text ESCAPE '\')
  AND (sqlc.narg(after_key)::text IS NULL
       OR (lower(s.product), i.id) > (sqlc.narg(after_key)::text, sqlc.narg(after_id)::uuid))
ORDER BY lower(s.product), i.id
LIMIT @page_size;

-- name: ListAssetChanges :many
-- O histórico de um ativo: instalações, remoções, mudanças de atributo, de
-- unidade e de etiqueta, na ordem em que o servidor as registrou.
SELECT * FROM change_log
WHERE tenant_id = @tenant_id AND asset_id = @asset_id AND id > @after_id
  AND (sqlc.narg(since)::timestamptz IS NULL OR occurred_at >= sqlc.narg(since)::timestamptz)
  AND (coalesce(cardinality(@kinds::text[]), 0) = 0 OR kind = ANY(@kinds::text[]))
ORDER BY id
LIMIT @page_size;

-- name: ListChanges :many
-- GET /v1/changes: o fluxo de mudanças do tenant, depois de um cursor.
SELECT * FROM change_log
WHERE tenant_id = @tenant_id AND id > @after_id
  AND (coalesce(cardinality(@kinds::text[]), 0) = 0 OR kind = ANY(@kinds::text[]))
ORDER BY id
LIMIT @page_size;

-- name: ListTenantSoftware :many
-- Os produtos instalados agora nos ativos do tenant, com quantos ativos e
-- versões, em ordem de produto.
SELECT s.id, s.product, s.tenant_id AS software_tenant_id, vd.name AS vendor,
       count(DISTINCT i.asset_id)::int AS asset_count,
       count(DISTINCT v.id)::int AS version_count
FROM installation i
JOIN asset a ON a.id = i.asset_id
JOIN software_version v ON v.id = i.software_version_id
JOIN software s ON s.id = v.software_id
LEFT JOIN vendor vd ON vd.id = s.vendor_id
WHERE a.tenant_id = @tenant_id AND i.removed_at IS NULL
  AND (@unit_mode::text = ''
       OR (@unit_mode::text = 'none' AND a.unit_id IS NULL)
       OR (@unit_mode::text = 'in' AND a.unit_id = ANY(@unit_ids::uuid[])))
  AND (sqlc.narg(product)::text IS NULL OR s.product ILIKE sqlc.narg(product)::text ESCAPE '\'
       OR vd.name ILIKE sqlc.narg(product)::text ESCAPE '\')
  AND (sqlc.narg(after_key)::text IS NULL
       OR (lower(s.product), s.id) > (sqlc.narg(after_key)::text, sqlc.narg(after_id)::uuid))
GROUP BY s.id, s.product, s.tenant_id, vd.name
ORDER BY lower(s.product), s.id
LIMIT @page_size;

-- name: GetVisibleSoftware :one
-- Um produto do catálogo comum ou do próprio tenant.
SELECT s.id, s.product, s.tenant_id AS software_tenant_id, vd.name AS vendor
FROM software s LEFT JOIN vendor vd ON vd.id = s.vendor_id
WHERE s.id = @id AND (s.tenant_id IS NULL OR s.tenant_id = @tenant_id);

-- name: ListSoftwareVersionsInTenant :many
-- As versões de um produto instaladas agora no tenant (com o mesmo filtro
-- de unidade da lista de instalações).
SELECT v.id, v.version, v.purl, count(DISTINCT i.asset_id)::int AS asset_count
FROM installation i
JOIN asset a ON a.id = i.asset_id
JOIN software_version v ON v.id = i.software_version_id
WHERE v.software_id = @software_id AND a.tenant_id = @tenant_id AND i.removed_at IS NULL
  AND (@unit_mode::text = ''
       OR (@unit_mode::text = 'none' AND a.unit_id IS NULL)
       OR (@unit_mode::text = 'in' AND a.unit_id = ANY(@unit_ids::uuid[])))
GROUP BY v.id, v.version, v.purl
ORDER BY v.version, v.id;

-- name: ListSoftwareInstallations :many
-- Em quais ativos do tenant um produto (ou uma versão dele) está agora.
SELECT i.id, i.install_path, v.id AS version_id, v.version, v.purl,
       a.id AS asset_id, a.hostname, a.unit_id,
       fs.collected_at AS installed_since, ls.collected_at AS last_seen_at
FROM installation i
JOIN software_version v ON v.id = i.software_version_id
JOIN asset a ON a.id = i.asset_id
JOIN snapshot fs ON fs.id = i.first_snapshot_id
JOIN snapshot ls ON ls.id = i.last_snapshot_id
WHERE v.software_id = @software_id AND a.tenant_id = @tenant_id AND i.removed_at IS NULL
  AND (sqlc.narg(version_id)::uuid IS NULL OR v.id = sqlc.narg(version_id)::uuid)
  AND (sqlc.narg(version)::text IS NULL OR v.version = sqlc.narg(version)::text)
  AND (@unit_mode::text = ''
       OR (@unit_mode::text = 'none' AND a.unit_id IS NULL)
       OR (@unit_mode::text = 'in' AND a.unit_id = ANY(@unit_ids::uuid[])))
  AND (sqlc.narg(after_key)::text IS NULL
       OR (lower(coalesce(a.hostname, '')), i.id) > (sqlc.narg(after_key)::text, sqlc.narg(after_id)::uuid))
ORDER BY lower(coalesce(a.hostname, '')), i.id
LIMIT @page_size;
