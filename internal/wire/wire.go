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
)

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

// EnrollResponse devolve o certificado do agente e a CA, que o agente passa
// a usar para validar o servidor.
type EnrollResponse struct {
	AgentID     uuid.UUID `json:"agent_id"`
	AssetID     uuid.UUID `json:"asset_id"`
	Certificate string    `json:"certificate"`
	CA          string    `json:"ca"`
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
}
