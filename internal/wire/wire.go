// Package wire tem os corpos e cabeçalhos HTTP trocados entre o agente e o
// servidor. Fica fora de internal/server/api para o agente não carregar as
// dependências do servidor (pgx, goose, validador de schema).
package wire

import (
	"github.com/google/uuid"

	"github.com/athosbes/invenire/pkg/snapshot"
)

// Rotas.
const (
	PathEnroll    = "/v1/enroll"
	PathSnapshots = "/v1/snapshots"
	// PathAgent devolve quem o crachá apresentado é, para o agente conferir
	// se um servidor novo o reconhece (set-server).
	PathAgent = "/v1/agent"
	// PathAgentRenew troca o crachá do agente por um novo, antes de vencer.
	PathAgentRenew = "/v1/agent/renew"
)

// RenewRequest é o corpo de POST /v1/agent/renew.
type RenewRequest struct {
	CSR string `json:"csr"` // PEM, CERTIFICATE REQUEST
}

// RenewResponse devolve o crachá novo, assinado pela CA do tenant.
type RenewResponse struct {
	Certificate string `json:"certificate"`
}

// SHA256Header leva o SHA-256 (hex) do snapshot descomprimido, exatamente
// como snapshot.Marshal o calculou.
const SHA256Header = "Invenire-Snapshot-SHA256"

// TokenPrefix inicia todo token de cadastro.
const TokenPrefix = "inv_"

// EnrollRequest é o corpo de POST /v1/enroll.
type EnrollRequest struct {
	Token        string        `json:"token"`
	CSR          string        `json:"csr"` // PEM, CERTIFICATE REQUEST
	AgentVersion string        `json:"agent_version"`
	Host         snapshot.Host `json:"host"`
	OS           snapshot.OS   `json:"os"`
}

// EnrollResponse devolve o crachá do agente, assinado pela CA do tenant, e a
// CA que assina o certificado do servidor, que o agente passa a usar para
// conferir o servidor. CA vem vazia quando o servidor usa certificado
// público: aí o agente confia nas CAs do sistema.
type EnrollResponse struct {
	AgentID     uuid.UUID `json:"agent_id"`
	AssetID     uuid.UUID `json:"asset_id"`
	Certificate string    `json:"certificate"`
	CA          string    `json:"ca,omitempty"`
}

// SnapshotResponse é a resposta de POST /v1/snapshots.
type SnapshotResponse struct {
	SnapshotID uuid.UUID `json:"snapshot_id"`
	SHA256     string    `json:"sha256"`
	// Duplicate indica que o snapshot já tinha sido recebido (reenvio da fila).
	Duplicate bool `json:"duplicate"`
}

// Error é o corpo de toda resposta de erro.
type Error struct {
	Error string `json:"error"`
	// Code identifica, para o agente, as recusas que mudam o que ele faz
	// (ErrorCode*). Vazio nas demais. Uma resposta sem Code pode não ter
	// vindo do servidor, e sim de um proxy no caminho.
	Code string `json:"code,omitempty"`
}

// Códigos de erro que o agente interpreta.
const (
	// ErrorCodeAgentRevoked: o crachá não corresponde a nenhum agente ativo
	// (revogado, ou a máquina foi cadastrada de novo). Só cadastrando de novo.
	ErrorCodeAgentRevoked = "agent_revoked"
	// ErrorCodeBadgeInvalid: o crachá é de um agente conhecido, mas venceu ou
	// não foi emitido pela CA (não revogada) do tenant. Só cadastrando de novo.
	ErrorCodeBadgeInvalid = "badge_invalid"
	// ErrorCodeTooLarge: o próprio servidor recusou o corpo pelo tamanho.
	// Reenviar o mesmo corpo não adianta. Um 413 sem este código é de um
	// proxy no caminho, e o agente tenta de novo depois.
	ErrorCodeTooLarge = "too_large"
)

// AgentInfo é a resposta de GET /v1/agent.
type AgentInfo struct {
	AgentID uuid.UUID `json:"agent_id"`
	AssetID uuid.UUID `json:"asset_id"`
}
