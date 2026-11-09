-- CA por tenant: cada tenant tem a sua própria CA, independente, que assina
-- os crachás (certificados cliente) dos agentes dele. A CA da instalação
-- (data/ca) passa a assinar só o certificado TLS do servidor.
--
-- A chave fica cifrada com a chave-mestra da instalação (AES-256-GCM). Uma
-- CA sem chave só serve para conferir: a CA antiga da instalação, importada
-- para os agentes cadastrados antes desta migração, ou uma CA do cliente.

-- +goose Up
CREATE TABLE tenant_ca (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenant(id),
  cert        bytea NOT NULL,          -- DER
  fingerprint text NOT NULL,           -- SHA-256 do DER, em hex
  key_sealed  bytea,                   -- nulo = só confere, não assina
  created_at  timestamptz NOT NULL DEFAULT now(),
  retired_at  timestamptz,             -- não assina mais crachás novos
  revoked_at  timestamptz,             -- não vale mais nem para conferir
  UNIQUE (tenant_id, fingerprint),
  CHECK (retired_at IS NOT NULL OR key_sealed IS NOT NULL),
  CHECK (revoked_at IS NULL OR retired_at IS NOT NULL)
);
-- No máximo uma CA assinando por tenant.
CREATE UNIQUE INDEX tenant_ca_signing ON tenant_ca (tenant_id) WHERE retired_at IS NULL;

-- Qual CA assinou o crachá do agente. Nulo só para agentes de antes desta
-- migração, até o serve importar a CA antiga da instalação.
ALTER TABLE agent ADD COLUMN tenant_ca_id uuid REFERENCES tenant_ca(id);
CREATE INDEX ON agent (tenant_ca_id);

-- +goose Down
ALTER TABLE agent DROP COLUMN tenant_ca_id;
DROP TABLE tenant_ca;
