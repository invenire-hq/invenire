-- Esquema v0: inventário (tenant, asset, agent, snapshot), identidade de
-- software (vendor, software, software_version, software_raw, installation)
-- e a base para agentes de IA (actor, proposal, change_log).

-- +goose Up
CREATE TABLE tenant (
  id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE
);

-- Quem age no sistema: pessoas, agentes de IA, pipelines e o próprio
-- servidor (regras de normalização, ingestão). Toda mudança é atribuída a um.
CREATE TABLE actor (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenant(id),
  kind        text NOT NULL CHECK (kind IN ('human','ai_agent','ci','collector','system')),
  name        text NOT NULL,
  scopes      text[] NOT NULL DEFAULT '{}',  -- read, propose, write:releases, admin
  created_at  timestamptz NOT NULL DEFAULT now(),
  disabled_at timestamptz,
  UNIQUE (tenant_id, kind, name)
);

CREATE TABLE asset (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenant(id),
  fingerprint   text NOT NULL,          -- smbios:<uuid> ou machine:<id>, ver snapshot.Fingerprint
  kind          text NOT NULL DEFAULT 'workstation'
                CHECK (kind IN ('workstation','server','vm','other')),
  hostname      text,
  serial        text,
  manufacturer  text,
  model         text,
  os_family     text,
  os_name       text,
  os_version    text,
  criticality   smallint,               -- preenchido a partir da v4
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, fingerprint)
);

CREATE TABLE agent (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  asset_id         uuid NOT NULL REFERENCES asset(id),
  cert_fingerprint text NOT NULL UNIQUE,  -- SHA-256 do certificado cliente
  agent_version    text NOT NULL,
  enrolled_at      timestamptz NOT NULL DEFAULT now(),
  last_seen_at     timestamptz,
  revoked_at       timestamptz
);
CREATE INDEX ON agent (asset_id);

-- Inventário bruto, imutável. A ingestão só preenche processed_at.
CREATE TABLE snapshot (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  asset_id       uuid NOT NULL REFERENCES asset(id),
  agent_id       uuid NOT NULL REFERENCES agent(id),
  schema_version text NOT NULL,
  collected_at   timestamptz NOT NULL,
  received_at    timestamptz NOT NULL DEFAULT now(),
  sha256         bytea NOT NULL UNIQUE CHECK (length(sha256) = 32),
  prev_sha256    bytea CHECK (length(prev_sha256) = 32),  -- encadeamento por ativo
  processed_at   timestamptz,
  payload        jsonb NOT NULL
);
CREATE INDEX ON snapshot (asset_id, collected_at DESC);
CREATE INDEX ON snapshot (received_at) WHERE processed_at IS NULL;

CREATE TABLE vendor (
  id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE             -- forma normalizada
);

CREATE TABLE software (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  vendor_id   uuid REFERENCES vendor(id),
  product     text NOT NULL,
  cpe_vendor  text,
  cpe_product text,
  -- Sem NULLS NOT DISTINCT, produtos sem fabricante se repetiriam.
  UNIQUE NULLS NOT DISTINCT (vendor_id, product)
);

CREATE TABLE software_version (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  software_id uuid NOT NULL REFERENCES software(id),
  version     text NOT NULL,
  purl        text UNIQUE,
  cpe         text,
  UNIQUE (software_id, version)
);

-- Liga o nome bruto de uma fonte a um produto. software_id nulo = ainda não
-- identificado; é daqui que sai a lista de software não identificado.
CREATE TABLE software_raw (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  source      text NOT NULL,            -- win_registry, dpkg, rpm, browser_ext, sbom
  raw_name    text NOT NULL,
  raw_vendor  text NOT NULL DEFAULT '',
  software_id uuid REFERENCES software(id),
  confidence  real CHECK (confidence BETWEEN 0 AND 1),
  mapped_by   uuid REFERENCES actor(id),  -- quem mapeou ou aprovou
  mapped_rule text,                       -- regra que fez o mapeamento, se houver
  mapped_at   timestamptz,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source, raw_name, raw_vendor),
  CHECK ((software_id IS NULL) = (mapped_at IS NULL))
);
CREATE INDEX ON software_raw (first_seen_at) WHERE software_id IS NULL;

-- Uma versão instalada num ativo enquanto removed_at é nulo. Uma
-- reinstalação depois da remoção abre uma linha nova, preservando o histórico.
CREATE TABLE installation (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  asset_id            uuid NOT NULL REFERENCES asset(id),
  software_version_id uuid NOT NULL REFERENCES software_version(id),
  install_path        text NOT NULL DEFAULT '',
  first_snapshot_id   uuid NOT NULL REFERENCES snapshot(id),
  last_snapshot_id    uuid NOT NULL REFERENCES snapshot(id),
  removed_at          timestamptz,
  removed_snapshot_id uuid REFERENCES snapshot(id),
  CHECK ((removed_at IS NULL) = (removed_snapshot_id IS NULL))
);
CREATE UNIQUE INDEX installation_open_key ON installation (asset_id, software_version_id, install_path)
  WHERE removed_at IS NULL;
CREATE INDEX ON installation (software_version_id) WHERE removed_at IS NULL;

-- Agentes propõem, humanos aprovam. VEX e criticidade nunca são aprovados
-- por regra (ver o documento de arquitetura).
CREATE TABLE proposal (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenant(id),
  actor_id    uuid NOT NULL REFERENCES actor(id),
  kind        text NOT NULL CHECK (kind IN ('software_mapping','vex','criticality')),
  target_type text NOT NULL,
  target_id   text NOT NULL,
  payload     jsonb NOT NULL,           -- o que muda
  evidence    text NOT NULL,            -- por que, em texto
  confidence  real CHECK (confidence BETWEEN 0 AND 1),
  status      text NOT NULL DEFAULT 'pending'
              CHECK (status IN ('pending','approved','rejected')),
  reviewed_by uuid REFERENCES actor(id),
  reviewed_at timestamptz,
  review_note text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CHECK ((status = 'pending') = (reviewed_at IS NULL)),
  CHECK ((status = 'pending') = (reviewed_by IS NULL))
);
CREATE INDEX ON proposal (tenant_id, created_at) WHERE status = 'pending';
CREATE INDEX ON proposal (target_type, target_id);

-- Fluxo de mudanças. id é o cursor de GET /v1/changes e o as_of das respostas.
CREATE TABLE change_log (
  id          bigserial PRIMARY KEY,
  tenant_id   uuid NOT NULL REFERENCES tenant(id),
  occurred_at timestamptz NOT NULL DEFAULT now(),
  kind        text NOT NULL,            -- software_installed, software_removed, asset_new,
                                        -- asset_stale, proposal_created, proposal_reviewed…
  asset_id    uuid REFERENCES asset(id),
  actor_id    uuid REFERENCES actor(id),
  data        jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX ON change_log (tenant_id, id);
CREATE INDEX ON change_log (asset_id, id) WHERE asset_id IS NOT NULL;

-- +goose Down
DROP TABLE change_log;
DROP TABLE proposal;
DROP TABLE installation;
DROP TABLE software_raw;
DROP TABLE software_version;
DROP TABLE software;
DROP TABLE vendor;
DROP TABLE snapshot;
DROP TABLE agent;
DROP TABLE asset;
DROP TABLE actor;
DROP TABLE tenant;
