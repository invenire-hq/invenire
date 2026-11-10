-- Token de cadastro com vários usos, e revogação.
--
-- Para um lote de máquinas (o piloto, um laboratório) um token por máquina é
-- trabalho demais; um token com max_uses cadastra até N máquinas. used_at e
-- used_by passam a ser o último uso; cada uso fica no change_log
-- (agent_enrolled traz o token_id). Um token de vários usos que vaza
-- cadastra máquinas estranhas no tenant: revoked_at o corta na hora.

-- +goose Up
ALTER TABLE enrollment_token ADD COLUMN max_uses int NOT NULL DEFAULT 1 CHECK (max_uses BETWEEN 1 AND 10000);
ALTER TABLE enrollment_token ADD COLUMN uses int NOT NULL DEFAULT 0;
ALTER TABLE enrollment_token ADD COLUMN revoked_at timestamptz;
UPDATE enrollment_token SET uses = 1 WHERE used_at IS NOT NULL;
ALTER TABLE enrollment_token ADD CONSTRAINT enrollment_token_uses CHECK (uses BETWEEN 0 AND max_uses);
ALTER TABLE enrollment_token ADD CONSTRAINT enrollment_token_used CHECK ((uses = 0) = (used_at IS NULL));
CREATE INDEX ON enrollment_token (tenant_id, created_at);

-- +goose Down
DROP INDEX enrollment_token_tenant_id_created_at_idx;
ALTER TABLE enrollment_token DROP CONSTRAINT enrollment_token_used;
ALTER TABLE enrollment_token DROP CONSTRAINT enrollment_token_uses;
ALTER TABLE enrollment_token DROP COLUMN revoked_at;
ALTER TABLE enrollment_token DROP COLUMN uses;
ALTER TABLE enrollment_token DROP COLUMN max_uses;
