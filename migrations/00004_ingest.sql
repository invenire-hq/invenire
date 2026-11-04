-- Ingestão: snapshots pendentes saem em ordem de coleta, e um snapshot que
-- não pôde ser ingerido fica marcado com o motivo em vez de travar a fila.

-- +goose Up
ALTER TABLE snapshot ADD COLUMN ingest_error text;
CREATE INDEX snapshot_pending ON snapshot (collected_at, received_at) WHERE processed_at IS NULL;
DROP INDEX snapshot_received_at_idx;

-- +goose Down
CREATE INDEX snapshot_received_at_idx ON snapshot (received_at) WHERE processed_at IS NULL;
DROP INDEX snapshot_pending;
ALTER TABLE snapshot DROP COLUMN ingest_error;
