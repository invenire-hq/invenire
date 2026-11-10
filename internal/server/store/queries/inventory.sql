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
-- O id vem de quem chama: ele vai no certificado antes da linha existir.
INSERT INTO agent (id, asset_id, cert_fingerprint, agent_version, tenant_ca_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetActiveAgentByCert :one
-- Usado na autenticação mTLS: agente revogado não é encontrado.
SELECT * FROM agent WHERE cert_fingerprint = $1 AND revoked_at IS NULL;

-- name: CreateEnrollmentToken :one
INSERT INTO enrollment_token (tenant_id, token_hash, description, created_by, expires_at, unit_id, max_uses)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, expires_at;

-- name: ConsumeEnrollmentToken :one
-- Reserva o token na transação do enrollment. FOR UPDATE impede que dois
-- cadastros simultâneos usem o mesmo token; MarkEnrollmentTokenUsed fecha.
SELECT id, tenant_id, unit_id FROM enrollment_token
WHERE token_hash = $1 AND uses < max_uses AND revoked_at IS NULL AND expires_at > now()
FOR UPDATE;

-- name: MarkEnrollmentTokenUsed :exec
UPDATE enrollment_token SET used_at = now(), used_by = $2, uses = uses + 1 WHERE id = $1;

-- name: TouchAgent :exec
UPDATE agent SET last_seen_at = now(), agent_version = $2 WHERE id = $1;

-- name: RevokeAgent :exec
UPDATE agent SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeAssetAgents :execrows
-- No recadastro: os crachás anteriores do ativo deixam de valer. Sem isso,
-- quem recadastra uma máquina (ou se passa por ela com um token) conviveria
-- com o agente antigo sem ninguém notar.
UPDATE agent SET revoked_at = now() WHERE asset_id = $1 AND revoked_at IS NULL;

-- name: InsertSnapshot :one
-- O SHA-256 é único: reenviar o mesmo snapshot devolve o existente.
-- body são os bytes exatos; payload, o mesmo conteúdo como jsonb.
INSERT INTO snapshot (asset_id, agent_id, schema_version, collected_at, sha256, prev_sha256, payload, body)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (sha256) DO UPDATE SET sha256 = EXCLUDED.sha256
RETURNING id, (xmax = 0)::boolean AS inserted;

-- name: GetLastSnapshotHash :one
-- Para conferir o prev_sha256 do snapshot que está chegando.
SELECT sha256 FROM snapshot
WHERE asset_id = $1
ORDER BY collected_at DESC, received_at DESC
LIMIT 1;

-- name: GetSnapshotBody :one
-- Os bytes originais, para reconferir o hash ou entregar numa perícia.
SELECT body FROM snapshot WHERE id = $1;

-- name: VerifySnapshotChain :many
-- Reconfere o histórico de um ativo só com os dados guardados: o hash de cada
-- body e se o prev_sha256 de cada snapshot é o sha256 do anterior.
SELECT id, collected_at,
       COALESCE(sha256(body) = sha256, false)::boolean AS body_ok,
       COALESCE(prev_sha256 IS NOT DISTINCT FROM lag(sha256) OVER w, false)::boolean AS chain_ok
FROM snapshot
WHERE asset_id = $1
WINDOW w AS (ORDER BY collected_at, received_at)
ORDER BY collected_at, received_at;

-- name: ListEnrollmentTokens :many
-- Tokens do tenant, dos mais novos aos mais velhos; all = incluir os gastos,
-- vencidos e revogados (include_all).
SELECT t.id, t.description, t.created_at, t.expires_at, t.used_at, t.uses, t.max_uses, t.revoked_at,
       u.id AS unit_id
FROM enrollment_token t LEFT JOIN org_unit u ON u.id = t.unit_id
WHERE t.tenant_id = @tenant_id
  AND (@include_all::boolean OR (t.uses < t.max_uses AND t.revoked_at IS NULL AND t.expires_at > now()))
ORDER BY t.created_at DESC, t.id;

-- name: RevokeEnrollmentToken :one
UPDATE enrollment_token SET revoked_at = now()
WHERE id = @id AND tenant_id = @tenant_id AND revoked_at IS NULL
RETURNING id, uses, max_uses;
