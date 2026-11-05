-- Normalização e mapeamento manual.
--
-- asset_software_raw: os nomes brutos do último snapshot aplicado de cada
-- ativo. Diz em quantos ativos um nome não identificado aparece (para a lista
-- de não identificados) e quais ativos refazer quando ele é mapeado.
--
-- asset.remap_pending: o mapeamento de um nome que o ativo tem mudou e as
-- instalações dele precisam ser recalculadas a partir do último snapshot.
--
-- api_token: tokens de usuários e pipelines (actor) para a API. Só o SHA-256
-- fica guardado.

-- +goose Up
CREATE TABLE asset_software_raw (
  asset_id        uuid NOT NULL REFERENCES asset(id),
  software_raw_id uuid NOT NULL REFERENCES software_raw(id),
  PRIMARY KEY (asset_id, software_raw_id)
);
CREATE INDEX ON asset_software_raw (software_raw_id);

ALTER TABLE asset ADD COLUMN remap_pending boolean NOT NULL DEFAULT false;
CREATE INDEX asset_remap_pending ON asset (id) WHERE remap_pending;
-- Ativos já ingeridos não têm asset_software_raw: a reaplicação o preenche.
UPDATE asset SET remap_pending = true
WHERE EXISTS (SELECT 1 FROM snapshot s WHERE s.asset_id = asset.id AND s.processed_at IS NOT NULL);

CREATE TABLE api_token (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id    uuid NOT NULL REFERENCES actor(id),
  token_hash  bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
  description text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz,               -- nulo = não expira
  revoked_at  timestamptz
);
CREATE INDEX ON api_token (actor_id);

-- +goose Down
DROP TABLE api_token;
DROP INDEX asset_remap_pending;
ALTER TABLE asset DROP COLUMN remap_pending;
DROP TABLE asset_software_raw;
