-- Organização do tenant: unidades (árvore) e etiquetas dos ativos.

-- name: CreateUnit :one
INSERT INTO org_unit (tenant_id, parent_id, kind, name)
VALUES (@tenant_id, sqlc.narg(parent_id), @kind, @name)
RETURNING *;

-- name: GetUnit :one
SELECT * FROM org_unit WHERE id = @id AND tenant_id = @tenant_id;

-- name: GetUnitForUpdate :one
SELECT * FROM org_unit WHERE id = @id AND tenant_id = @tenant_id FOR UPDATE;

-- name: GetUnitByName :one
-- Um nível do caminho: o filho de parent_id (nulo = raiz) com esse nome.
SELECT * FROM org_unit
WHERE tenant_id = @tenant_id AND parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id) AND name = @name;

-- name: ListUnits :many
-- A árvore inteira do tenant, com o caminho e os ativos ligados direto a
-- cada unidade, em ordem de caminho.
WITH RECURSIVE tree AS (
  SELECT u.id, u.name::text AS path, 0 AS depth
  FROM org_unit u WHERE u.tenant_id = @tenant_id AND u.parent_id IS NULL
  UNION ALL
  SELECT c.id, t.path || '/' || c.name, t.depth + 1
  FROM org_unit c JOIN tree t ON c.parent_id = t.id
)
SELECT u.id, u.parent_id, u.kind, u.name, u.source, u.created_at,
       t.path::text AS path, t.depth::int AS depth,
       (SELECT count(*) FROM asset a WHERE a.unit_id = u.id)::int AS asset_count
FROM tree t JOIN org_unit u ON u.id = t.id
ORDER BY t.path;

-- name: UpdateUnit :one
UPDATE org_unit SET parent_id = sqlc.narg(parent_id), kind = @kind, name = @name
WHERE id = @id AND tenant_id = @tenant_id
RETURNING *;

-- name: UnitUsage :one
-- O que impede apagar uma unidade: subunidades, ativos e tokens de cadastro
-- ainda utilizáveis.
SELECT
  (SELECT count(*) FROM org_unit c WHERE c.parent_id = @id)::int AS children,
  (SELECT count(*) FROM asset a WHERE a.unit_id = @id)::int AS assets,
  (SELECT count(*) FROM enrollment_token t
    WHERE t.unit_id = @id AND t.uses < t.max_uses AND t.revoked_at IS NULL AND t.expires_at > now())::int AS pending_tokens;

-- name: DetachUnitTokens :exec
-- Tokens usados ou vencidos perdem a unidade apagada; o cadastro já
-- registrou onde a máquina entrou.
UPDATE enrollment_token SET unit_id = NULL WHERE unit_id = @id;

-- name: DeleteUnit :exec
DELETE FROM org_unit WHERE id = @id AND tenant_id = @tenant_id;

-- name: GetAssetInTenantForUpdate :one
SELECT * FROM asset WHERE id = @id AND tenant_id = @tenant_id FOR UPDATE;

-- name: SetAssetUnit :exec
UPDATE asset SET unit_id = sqlc.narg(unit_id) WHERE id = @id;

-- name: SetAssetUnitIfNone :one
-- No cadastro: a unidade do token só vale para ativo ainda sem unidade.
-- Recadastrar não tira a máquina do lugar que alguém escolheu.
UPDATE asset SET unit_id = @unit_id WHERE id = @id AND unit_id IS NULL
RETURNING id;

-- name: GetAssetLabel :one
SELECT * FROM asset_label WHERE asset_id = @asset_id AND key = @key;

-- name: SetAssetLabel :exec
INSERT INTO asset_label (asset_id, key, value, set_by)
VALUES (@asset_id, @key, @value, @set_by)
ON CONFLICT (asset_id, key) DO UPDATE SET value = EXCLUDED.value, set_by = EXCLUDED.set_by, set_at = now();

-- name: DeleteAssetLabel :one
DELETE FROM asset_label WHERE asset_id = @asset_id AND key = @key
RETURNING value;

-- name: ListAssetLabels :many
SELECT * FROM asset_label WHERE asset_id = @asset_id ORDER BY key;

-- name: LockUnitTree :exec
-- Serializa as mudanças na árvore de um tenant: dois movimentos simultâneos
-- (A para dentro de B e B para dentro de A) não podem passar os dois pela
-- conferência de ciclo.
SELECT pg_advisory_xact_lock(hashtextextended('org_unit:' || sqlc.arg(tenant_id)::uuid::text, 0));
