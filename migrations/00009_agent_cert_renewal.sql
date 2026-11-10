-- Renovação do crachá do agente (POST /v1/agent/renew).
--
-- O crachá vale dois anos; o agente pede outro antes de vencer. O anterior
-- continua aceito até vencer sozinho: se a resposta da renovação se perder no
-- caminho, o agente ainda tem com o que se apresentar e pede de novo.

-- +goose Up
ALTER TABLE agent ADD COLUMN prev_cert_fingerprint text UNIQUE;
ALTER TABLE agent ADD COLUMN prev_tenant_ca_id uuid REFERENCES tenant_ca(id);
ALTER TABLE agent ADD CONSTRAINT agent_prev_cert_pair
  CHECK ((prev_cert_fingerprint IS NULL) = (prev_tenant_ca_id IS NULL));

-- +goose Down
ALTER TABLE agent DROP CONSTRAINT agent_prev_cert_pair;
ALTER TABLE agent DROP COLUMN prev_tenant_ca_id;
ALTER TABLE agent DROP COLUMN prev_cert_fingerprint;
