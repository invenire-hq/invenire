-- Renovação do crachá por requisição assinada (servidor atrás de proxy).
--
-- A assinatura não tem nonce: repetir um snapshot ou um GET /v1/agent é
-- inofensivo. Repetir uma renovação não é: quem capturou "renovar com o
-- crachá A" poderia reenviá-la depois de o agente já usar o crachá B, e a
-- troca derrubaria B, deixando o agente de fora. Por isso o servidor guarda o
-- timestamp assinado da última renovação e só aceita outra com timestamp
-- maior. A renovação por mTLS não tem timestamp e não passa por aqui.

-- +goose Up
ALTER TABLE agent ADD COLUMN last_renew_signed_at timestamptz;

-- +goose Down
ALTER TABLE agent DROP COLUMN last_renew_signed_at;
