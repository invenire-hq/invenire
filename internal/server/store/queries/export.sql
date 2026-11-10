-- Exportação do tenant: tudo o que é dele e não pode ser recalculado.
-- Instalações, mapeamentos de regra e software_raw são derivados dos
-- snapshots e não saem; quem importa os recalcula.

-- name: ExportTenantCAs :many
SELECT * FROM tenant_ca WHERE tenant_id = @tenant_id ORDER BY created_at, id;

-- name: ExportUnits :many
SELECT * FROM org_unit WHERE tenant_id = @tenant_id ORDER BY created_at, id;

-- name: ExportAssets :many
SELECT * FROM asset WHERE tenant_id = @tenant_id ORDER BY first_seen_at, id;

-- name: ExportLabels :many
SELECT l.* FROM asset_label l JOIN asset a ON a.id = l.asset_id
WHERE a.tenant_id = @tenant_id ORDER BY l.asset_id, l.key;

-- name: ExportAgents :many
SELECT g.* FROM agent g JOIN asset a ON a.id = g.asset_id
WHERE a.tenant_id = @tenant_id ORDER BY g.enrolled_at, g.id;

-- name: ExportActors :many
SELECT * FROM actor WHERE tenant_id = @tenant_id ORDER BY created_at, id;

-- name: ExportSnapshots :many
-- Só os metadados; os bytes saem um a um por GetSnapshotBody. has_body é
-- falso nos snapshots recebidos antes da migração 3, sem os bytes originais.
SELECT s.id, s.asset_id, s.agent_id, s.schema_version, s.collected_at, s.received_at, s.sha256, s.prev_sha256,
       (s.body IS NOT NULL)::boolean AS has_body
FROM snapshot s JOIN asset a ON a.id = s.asset_id
WHERE a.tenant_id = @tenant_id
ORDER BY s.asset_id, s.collected_at, s.received_at, s.id;

-- name: ExportPrivateSoftware :many
-- Produtos criados à mão pelo tenant (os do catálogo comum vêm das regras).
SELECT s.id, s.product, s.cpe_vendor, s.cpe_product, v.id AS vendor_id, v.name AS vendor, v.tenant_id AS vendor_tenant_id
FROM software s LEFT JOIN vendor v ON v.id = s.vendor_id
WHERE s.tenant_id = @tenant_id
ORDER BY s.product, s.id;

-- name: ExportMappings :many
-- Mapeamentos manuais: o nome bruto vai por extenso (fonte, nome,
-- fabricante), porque é por ele que quem importa reencontra o software_raw.
SELECT r.source, r.raw_name, r.raw_vendor, m.software_id, s.product, v.name AS vendor,
       s.tenant_id AS software_tenant_id, m.mapped_by, m.mapped_at
FROM tenant_software_map m
JOIN software_raw r ON r.id = m.software_raw_id
JOIN software s ON s.id = m.software_id
LEFT JOIN vendor v ON v.id = s.vendor_id
WHERE m.tenant_id = @tenant_id
ORDER BY r.source, r.raw_name, r.raw_vendor;

-- name: ExportProposals :many
SELECT * FROM proposal WHERE tenant_id = @tenant_id ORDER BY created_at, id;

-- name: GetSnapshotPayload :one
-- O snapshot como jsonb, para os que não têm os bytes originais.
SELECT payload FROM snapshot WHERE id = @id;
