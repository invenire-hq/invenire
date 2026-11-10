-- Situação do parque de um tenant (invenire-server status): o painel do
-- piloto.

-- name: TenantStatus :many
-- Uma linha por ativo, com o agente ativo e a saúde dos snapshots.
SELECT a.id, a.hostname, a.os_family, a.unit_id,
       g.id AS agent_id, g.agent_version, g.last_seen_at AS agent_last_seen,
       (SELECT max(s.collected_at) FROM snapshot s WHERE s.asset_id = a.id) AS last_collected,
       (SELECT count(*) FROM snapshot s WHERE s.asset_id = a.id AND s.processed_at IS NULL AND s.ingest_error IS NULL)::int AS pending,
       (SELECT count(*) FROM snapshot s WHERE s.asset_id = a.id AND s.ingest_error IS NOT NULL)::int AS failed,
       (SELECT count(*) FROM change_log c WHERE c.asset_id = a.id AND c.kind = 'snapshot_chain_broken')::int AS chain_breaks,
       (SELECT count(*) FROM installation i WHERE i.asset_id = a.id AND i.removed_at IS NULL)::int AS installed
FROM asset a
LEFT JOIN agent g ON g.asset_id = a.id AND g.revoked_at IS NULL
WHERE a.tenant_id = @tenant_id
ORDER BY lower(coalesce(a.hostname, '')), a.id;

-- name: CountUnmappedForTenant :one
-- Quantos nomes sem produto o tenant tem (a lista é GET /v1/software/unmapped).
SELECT count(DISTINCT r.id)::int
FROM software_raw r
JOIN asset_software_raw ar ON ar.software_raw_id = r.id
JOIN asset a ON a.id = ar.asset_id
WHERE r.software_id IS NULL AND a.tenant_id = @tenant_id
  AND NOT EXISTS (SELECT 1 FROM tenant_software_map m
                  WHERE m.tenant_id = a.tenant_id AND m.software_raw_id = r.id);

-- name: RecentIngestErrors :many
-- Os snapshots que a ingestão não conseguiu aplicar, com o motivo.
SELECT s.id, s.asset_id, a.hostname, s.collected_at, s.ingest_error
FROM snapshot s JOIN asset a ON a.id = s.asset_id
WHERE a.tenant_id = @tenant_id AND s.ingest_error IS NOT NULL
ORDER BY s.received_at DESC
LIMIT 10;
