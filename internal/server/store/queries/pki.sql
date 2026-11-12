-- name: GetSigningCA :one
SELECT * FROM tenant_ca WHERE tenant_id = $1 AND retired_at IS NULL;

-- name: InsertSigningCA :one
-- Duas criações ao mesmo tempo: só uma vence; a outra relê com GetSigningCA.
INSERT INTO tenant_ca (tenant_id, cert, fingerprint, key_sealed)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id) WHERE retired_at IS NULL DO NOTHING
RETURNING *;

-- name: ImportVerifyOnlyCA :one
-- CA que só confere (sem chave), já aposentada para assinar.
INSERT INTO tenant_ca (tenant_id, cert, fingerprint, retired_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (tenant_id, fingerprint) DO UPDATE SET fingerprint = EXCLUDED.fingerprint
RETURNING *;

-- name: TenantsWithLegacyAgents :many
SELECT DISTINCT a.tenant_id FROM agent g JOIN asset a ON a.id = g.asset_id
WHERE g.tenant_ca_id IS NULL;

-- name: AssignLegacyAgents :execrows
UPDATE agent g SET tenant_ca_id = $2
FROM asset a
WHERE a.id = g.asset_id AND a.tenant_id = $1 AND g.tenant_ca_id IS NULL;

-- name: GetAgentForAuth :one
-- Autenticação mTLS: o agente pelo crachá, com a CA que o assinou. Agente
-- revogado, CA revogada ou agente sem CA não são encontrados. Vale o crachá
-- atual e o anterior à última renovação, cada um com a sua CA.
SELECT sqlc.embed(g), c.cert AS ca_cert
FROM agent g
JOIN tenant_ca c ON c.id = CASE WHEN g.cert_fingerprint = sqlc.arg(fingerprint)::text
                                THEN g.tenant_ca_id ELSE g.prev_tenant_ca_id END
WHERE (g.cert_fingerprint = sqlc.arg(fingerprint)::text OR g.prev_cert_fingerprint = sqlc.arg(fingerprint)::text)
  AND g.revoked_at IS NULL AND c.revoked_at IS NULL;

-- name: RenewAgentCert :one
-- Troca o crachá do agente. presented é o crachá com que ele se apresentou:
-- se for o atual, vira o anterior; se já for o anterior (a resposta da
-- renovação passada se perdeu), continua sendo, e só o atual é trocado.
-- signed_at é o timestamp da requisição assinada (nulo no mTLS): só passa se
-- for maior que o da última renovação assinada, o que barra a repetição de
-- uma renovação capturada.
UPDATE agent SET
  prev_cert_fingerprint = CASE WHEN cert_fingerprint = sqlc.arg(presented)::text THEN cert_fingerprint ELSE prev_cert_fingerprint END,
  prev_tenant_ca_id     = CASE WHEN cert_fingerprint = sqlc.arg(presented)::text THEN tenant_ca_id ELSE prev_tenant_ca_id END,
  cert_fingerprint      = sqlc.arg(cert_fingerprint),
  tenant_ca_id          = sqlc.arg(tenant_ca_id),
  last_renew_signed_at  = coalesce(sqlc.narg(signed_at)::timestamptz, last_renew_signed_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
  AND (cert_fingerprint = sqlc.arg(presented)::text OR prev_cert_fingerprint = sqlc.arg(presented)::text)
  AND (sqlc.narg(signed_at)::timestamptz IS NULL OR last_renew_signed_at IS NULL
       OR last_renew_signed_at < sqlc.narg(signed_at)::timestamptz)
RETURNING id;

-- name: RevokeTenantCA :exec
UPDATE tenant_ca SET retired_at = coalesce(retired_at, now()), revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: GetTenant :one
SELECT * FROM tenant WHERE id = $1;
