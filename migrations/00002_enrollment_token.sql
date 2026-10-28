-- Tokens de cadastro de agentes: uso único, com validade. Só o SHA-256 do
-- token fica no banco; o valor é mostrado uma vez, na criação.

-- +goose Up
CREATE TABLE enrollment_token (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenant(id),
  token_hash  bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
  description text NOT NULL DEFAULT '',
  created_by  uuid REFERENCES actor(id),
  created_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  used_by     uuid REFERENCES agent(id),
  CHECK ((used_at IS NULL) = (used_by IS NULL))
);

-- +goose Down
DROP TABLE enrollment_token;
