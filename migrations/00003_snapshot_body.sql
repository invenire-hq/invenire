-- Bytes originais do snapshot, exatamente como o agente os serializou.
--
-- payload (jsonb) serve para consultar, mas o PostgreSQL reescreve o JSON e o
-- SHA-256 deixa de poder ser recalculado a partir dele. body guarda o corpo
-- original: o CHECK garante que ele bate com sha256, e o encadeamento por
-- prev_sha256 pode ser reconferido a qualquer momento só com SQL.
--
-- Fica nulo apenas nos snapshots recebidos antes desta migração, cujos bytes
-- não existem mais.

-- +goose Up
ALTER TABLE snapshot ADD COLUMN body bytea COMPRESSION lz4
  CHECK (body IS NULL OR sha256(body) = sha256);

-- +goose Down
ALTER TABLE snapshot DROP COLUMN body;
