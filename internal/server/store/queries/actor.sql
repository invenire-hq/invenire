-- Atores, propostas e o fluxo de mudanças.

-- name: CreateActor :one
INSERT INTO actor (tenant_id, kind, name, scopes)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: EnsureSystemActor :one
-- O ator do próprio servidor (ingestão, regras), um por tenant.
INSERT INTO actor (tenant_id, kind, name)
VALUES ($1, 'system', $2)
ON CONFLICT (tenant_id, kind, name) DO UPDATE SET name = EXCLUDED.name
RETURNING *;

-- name: GetActiveActor :one
SELECT * FROM actor WHERE id = $1 AND disabled_at IS NULL;

-- name: DisableActor :exec
UPDATE actor SET disabled_at = now() WHERE id = $1 AND disabled_at IS NULL;

-- name: CreateProposal :one
INSERT INTO proposal (tenant_id, actor_id, kind, target_type, target_id, payload, evidence, confidence)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetProposal :one
SELECT * FROM proposal WHERE id = $1;

-- name: ListPendingProposals :many
SELECT * FROM proposal
WHERE tenant_id = $1 AND status = 'pending'
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
ORDER BY created_at, id
LIMIT $2;

-- name: ReviewProposal :one
-- Só decide propostas pendentes; sem linha devolvida, alguém decidiu antes.
UPDATE proposal SET
  status      = $2,
  reviewed_by = $3,
  review_note = $4,
  reviewed_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: AppendChange :one
INSERT INTO change_log (tenant_id, kind, asset_id, actor_id, data)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: ListChangesAfter :many
-- GET /v1/changes?after=<cursor>.
SELECT * FROM change_log
WHERE tenant_id = $1 AND id > $2
ORDER BY id
LIMIT $3;

-- name: LatestChangeID :one
-- O as_of das respostas: o último change_log.id que elas consideram.
SELECT COALESCE(max(id), 0)::bigint FROM change_log WHERE tenant_id = $1;
