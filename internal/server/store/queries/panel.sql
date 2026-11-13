-- Painel web: login de pessoa, sessões, primeira configuração e a tela de
-- acesso (usuários e tokens de API).

-- name: CountTenants :one
SELECT count(*)::int FROM tenant;

-- name: LockSetup :exec
-- Serializa a primeira configuração: duas abas enviando o /setup ao mesmo
-- tempo não criam duas organizações.
SELECT pg_advisory_xact_lock(hashtextextended('invenire:setup', 0));

-- name: CreateLogin :one
INSERT INTO login (email, password_hash) VALUES (@email, @password_hash)
RETURNING *;

-- name: GetLoginByEmail :one
SELECT * FROM login WHERE email = @email;

-- name: SetLoginPassword :exec
UPDATE login SET password_hash = @password_hash, password_changed_at = now() WHERE id = @id;

-- name: SetActorLogin :exec
UPDATE actor SET login_id = @login_id WHERE id = @id;

-- name: ListLoginActors :many
-- Os atores ativos de um login, um por tenant, para escolher a organização.
SELECT a.id, a.tenant_id, a.name, a.scopes, t.name AS tenant_name
FROM actor a JOIN tenant t ON t.id = a.tenant_id
WHERE a.login_id = @login_id AND a.disabled_at IS NULL
ORDER BY t.name, a.id;

-- name: CreateSession :exec
INSERT INTO login_session (id_hash, login_id, actor_id, expires_at)
VALUES (@id_hash, @login_id, @actor_id, @expires_at);

-- name: GetSession :one
-- A sessão válida, com o ator (ativo e ainda ligado ao mesmo login), o
-- tenant e o e-mail. Ator desativado ou desligado do login derruba a sessão.
SELECT s.id_hash, s.login_id, s.created_at AS session_created_at, s.last_seen_at, s.expires_at,
       sqlc.embed(a), t.name AS tenant_name, l.email
FROM login_session s
JOIN actor a ON a.id = s.actor_id
JOIN tenant t ON t.id = a.tenant_id
JOIN login l ON l.id = s.login_id
WHERE s.id_hash = @id_hash AND s.expires_at > now()
  AND a.disabled_at IS NULL AND a.login_id = s.login_id;

-- name: TouchSession :exec
UPDATE login_session SET last_seen_at = now(), expires_at = @expires_at WHERE id_hash = @id_hash;

-- name: DeleteSession :exec
DELETE FROM login_session WHERE id_hash = @id_hash;

-- name: DeleteLoginSessions :execrows
-- Troca de senha: todas as sessões do login caem.
DELETE FROM login_session WHERE login_id = @login_id;

-- name: DeleteActorSessions :execrows
DELETE FROM login_session WHERE actor_id = @actor_id;

-- name: DeleteExpiredSessions :execrows
DELETE FROM login_session WHERE expires_at <= now();

-- name: GetActorInTenant :one
SELECT * FROM actor WHERE id = @id AND tenant_id = @tenant_id;

-- name: DisableActorInTenant :one
UPDATE actor SET disabled_at = now()
WHERE id = @id AND tenant_id = @tenant_id AND disabled_at IS NULL
RETURNING *;

-- name: CountPanelAdmins :one
-- Administradores ativos com login: o último não pode ser desativado.
SELECT count(*)::int FROM actor
WHERE tenant_id = @tenant_id AND login_id IS NOT NULL AND disabled_at IS NULL AND 'admin' = ANY(scopes);

-- name: ListPanelUsers :many
-- Pessoas com login no painel do tenant.
SELECT a.id, a.name, a.scopes, a.created_at, a.disabled_at, l.email
FROM actor a JOIN login l ON l.id = a.login_id
WHERE a.tenant_id = @tenant_id
ORDER BY a.disabled_at IS NOT NULL, lower(a.name), a.id;

-- name: ListTenantAPITokens :many
-- Tokens de API do tenant, dos mais novos aos mais velhos.
SELECT t.id, t.description, t.created_at, t.expires_at, t.revoked_at,
       a.id AS actor_id, a.name AS actor_name, a.kind AS actor_kind, a.scopes, a.disabled_at AS actor_disabled_at
FROM api_token t JOIN actor a ON a.id = t.actor_id
WHERE a.tenant_id = @tenant_id
ORDER BY t.created_at DESC, t.id;

-- name: RevokeAPITokenInTenant :one
UPDATE api_token t SET revoked_at = now()
FROM actor a
WHERE t.id = @id AND a.id = t.actor_id AND a.tenant_id = @tenant_id AND t.revoked_at IS NULL
RETURNING t.id, t.actor_id;

-- name: ListTenantManualMappings :many
-- Os mapeamentos manuais do tenant, para desfazer pelo painel.
SELECT m.software_raw_id, r.source, r.raw_name, r.raw_vendor, m.mapped_at,
       s.id AS software_id, s.product, v.name AS vendor
FROM tenant_software_map m
JOIN software_raw r ON r.id = m.software_raw_id
JOIN software s ON s.id = m.software_id
LEFT JOIN vendor v ON v.id = s.vendor_id
WHERE m.tenant_id = @tenant_id
ORDER BY lower(r.raw_name), r.id;

-- name: GetAssetAgentToRevoke :one
-- O agente ativo do ativo do tenant, para revogar pelo painel.
SELECT g.id FROM agent g JOIN asset a ON a.id = g.asset_id
WHERE a.id = @asset_id AND a.tenant_id = @tenant_id AND g.revoked_at IS NULL
LIMIT 1;

-- name: ListAssetChangesRecent :many
-- O histórico de um ativo do mais novo para o mais velho, para o painel
-- (a rota /v1 anda para a frente, por cursor). before_id 0 = do fim.
SELECT * FROM change_log
WHERE tenant_id = @tenant_id AND asset_id = @asset_id
  AND (@before_id::bigint = 0 OR id < @before_id::bigint)
  AND (coalesce(cardinality(@kinds::text[]), 0) = 0 OR kind = ANY(@kinds::text[]))
ORDER BY id DESC
LIMIT @page_size;
