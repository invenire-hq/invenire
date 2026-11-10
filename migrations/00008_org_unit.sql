-- Organização dentro do tenant: unidades em árvore livre e etiquetas.
--
-- A unidade é uma camada à parte, ligada ao ativo: trocar a máquina de setor
-- muda só a ligação (e vira asset_moved no change_log). Profundidade e nomes
-- das camadas (secretaria, site, setor…) são do cliente. As chaves
-- estrangeiras compostas com tenant_id garantem no banco que unidade, ativo e
-- token são sempre do mesmo tenant.

-- +goose Up
CREATE TABLE org_unit (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL REFERENCES tenant(id),
  parent_id  uuid,                          -- nulo = raiz do tenant
  kind       text NOT NULL DEFAULT '',      -- definido pelo cliente: secretaria, site, setor…
  name       text NOT NULL,
  source     text NOT NULL DEFAULT 'manual',-- manual, glpi, ad, entra
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES org_unit (tenant_id, id),
  CHECK (parent_id <> id),
  -- "/" separa os níveis do caminho (Saúde/UBS Centro/Recepção).
  CHECK (name = btrim(name) AND name <> '' AND length(name) <= 120 AND strpos(name, '/') = 0),
  CHECK (length(kind) <= 60)
);
CREATE UNIQUE INDEX org_unit_name ON org_unit (tenant_id, parent_id, name) NULLS NOT DISTINCT;
CREATE INDEX ON org_unit (parent_id);

ALTER TABLE asset ADD COLUMN unit_id uuid;
ALTER TABLE asset ADD FOREIGN KEY (tenant_id, unit_id) REFERENCES org_unit (tenant_id, id);
CREATE INDEX ON asset (unit_id);

-- O token de cadastro leva a unidade: a máquina já nasce no lugar certo.
ALTER TABLE enrollment_token ADD COLUMN unit_id uuid;
ALTER TABLE enrollment_token ADD FOREIGN KEY (tenant_id, unit_id) REFERENCES org_unit (tenant_id, id);

-- Dimensões que não são hierarquia: ambiente=produção, dados=pessoais.
CREATE TABLE asset_label (
  asset_id uuid NOT NULL REFERENCES asset(id),
  key      text NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9_.-]{0,62}$'),
  value    text NOT NULL CHECK (length(value) <= 256),
  set_by   uuid REFERENCES actor(id),
  set_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (asset_id, key)
);
CREATE INDEX ON asset_label (key, value);

-- +goose Down
DROP TABLE asset_label;
ALTER TABLE enrollment_token DROP COLUMN unit_id;
ALTER TABLE asset DROP COLUMN unit_id;
DROP TABLE org_unit;
