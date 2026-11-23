// SPDX-License-Identifier: AGPL-3.0-only

// Package export lê e escreve o pacote de exportação de um tenant: o
// "git clone" do Invenire.
//
// O pacote é um .tar.gz com um arquivo JSON Lines por tipo de registro, os
// bytes exatos de cada snapshot em snapshots/<sha256>.json e, por último,
// manifest.json com o SHA-256 e a contagem de cada arquivo. Só sai o que não
// pode ser recalculado: instalações, software_raw e mapeamentos de regra são
// derivados dos snapshots, e quem importa os recalcula.
//
// O formato é contrato público (format_version): campos novos podem
// aparecer numa versão compatível; renomear ou mudar o sentido de um campo
// exige format_version nova.
package export

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	// Format identifica o pacote em manifest.json.
	Format = "invenire-tenant-export"
	// Version é a versão do formato que este código escreve e lê.
	Version = 1

	ManifestFile  = "manifest.json"
	SnapshotDir   = "snapshots/"
	PayloadSuffix = ".payload.json"
)

// Arquivos de registros, na ordem em que entram no pacote.
const (
	FileTenantCAs = "tenant_ca.jsonl"
	FileUnits     = "units.jsonl"
	FileAssets    = "assets.jsonl"
	FileLabels    = "labels.jsonl"
	FileAgents    = "agents.jsonl"
	FileActors    = "actors.jsonl"
	FileSnapshots = "snapshots.jsonl"
	FileProducts  = "products.jsonl"
	FileMappings  = "mappings.jsonl"
	FileProposals = "proposals.jsonl"
	FileChanges   = "changes.jsonl"
)

// Files são todos os arquivos de registros do formato 1.
var Files = []string{
	FileTenantCAs, FileUnits, FileAssets, FileLabels, FileAgents, FileActors,
	FileSnapshots, FileProducts, FileMappings, FileProposals, FileChanges,
}

// Manifest é manifest.json, o último arquivo do pacote.
type Manifest struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"format_version"`
	Tenant        struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	} `json:"tenant"`
	ExportedAt    time.Time `json:"exported_at"`
	ServerVersion string    `json:"server_version"`
	// AsOf é o último change_log.id do tenant no instante da exportação: tudo
	// no pacote foi lido nesse mesmo instante.
	AsOf int64 `json:"as_of"`
	// Files traz, para cada arquivo de registros, o SHA-256 (hex), o tamanho e
	// quantas linhas tem.
	Files map[string]FileInfo `json:"files"`
	// SnapshotBodies é quantos arquivos há em snapshots/; o nome de cada um é
	// o SHA-256 do conteúdo.
	SnapshotBodies int `json:"snapshot_bodies"`
	// SnapshotPayloads é quantos snapshots vieram só como payload, sem os
	// bytes originais (snapshots/<sha256>.payload.json).
	SnapshotPayloads int `json:"snapshot_payloads"`
	// CAKey, se presente, diz como as chaves das CAs foram cifradas.
	CAKey *CAKeyInfo `json:"ca_key,omitempty"`
}

// FileInfo descreve um arquivo de registros.
type FileInfo struct {
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	Records int    `json:"records"`
}

// CAKeyInfo descreve a cifragem de key_sealed em tenant_ca.jsonl: AES-256-GCM
// com a chave de exportação (32 bytes, mostrada uma vez a quem exportou),
// nonce de 12 bytes no início, e dados adicionais = id do tenant (16 bytes)
// seguido do id da CA (16 bytes).
type CAKeyInfo struct {
	Algorithm string `json:"algorithm"` // "AES-256-GCM"
	AAD       string `json:"aad"`       // "tenant_id||tenant_ca_id"
}

// TenantCA é uma CA que assinou crachás de agentes do tenant.
type TenantCA struct {
	ID          uuid.UUID  `json:"id"`
	Fingerprint string     `json:"fingerprint"`
	CertPEM     string     `json:"cert_pem"`
	CreatedAt   time.Time  `json:"created_at"`
	RetiredAt   *time.Time `json:"retired_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	// KeySealed é a chave privada (PKCS#8) cifrada com a chave de exportação,
	// em base64; ausente sem --with-ca-key ou numa CA só de conferência.
	KeySealed []byte `json:"key_sealed,omitempty"`
}

// Unit é uma unidade. A mãe pode aparecer depois da filha.
type Unit struct {
	ID        uuid.UUID  `json:"id"`
	ParentID  *uuid.UUID `json:"parent_id"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	Source    string     `json:"source"`
	CreatedAt time.Time  `json:"created_at"`
}

// Asset é um ativo. Collected é o que a máquina reportou no último snapshot
// ingerido; quem importa pode recalcular a partir dos snapshots.
type Asset struct {
	ID          uuid.UUID      `json:"id"`
	Fingerprint string         `json:"fingerprint"`
	Kind        string         `json:"kind"`
	UnitID      *uuid.UUID     `json:"unit_id"`
	Criticality *int16         `json:"criticality"`
	FirstSeenAt time.Time      `json:"first_seen_at"`
	LastSeenAt  time.Time      `json:"last_seen_at"`
	Collected   AssetCollected `json:"collected"`
}

// AssetCollected são os atributos reportados pela máquina.
type AssetCollected struct {
	Hostname     *string `json:"hostname"`
	Serial       *string `json:"serial"`
	Manufacturer *string `json:"manufacturer"`
	Model        *string `json:"model"`
	OSFamily     *string `json:"os_family"`
	OSName       *string `json:"os_name"`
	OSVersion    *string `json:"os_version"`
}

// Label é uma etiqueta de um ativo.
type Label struct {
	AssetID uuid.UUID  `json:"asset_id"`
	Key     string     `json:"key"`
	Value   string     `json:"value"`
	SetBy   *uuid.UUID `json:"set_by"`
	SetAt   time.Time  `json:"set_at"`
}

// Agent é um agente cadastrado (ativo ou revogado).
type Agent struct {
	ID                  uuid.UUID  `json:"id"`
	AssetID             uuid.UUID  `json:"asset_id"`
	CertFingerprint     string     `json:"cert_fingerprint"`
	TenantCAID          *uuid.UUID `json:"tenant_ca_id"`
	PrevCertFingerprint *string    `json:"prev_cert_fingerprint"`
	PrevTenantCAID      *uuid.UUID `json:"prev_tenant_ca_id"`
	AgentVersion        string     `json:"agent_version"`
	EnrolledAt          time.Time  `json:"enrolled_at"`
	LastSeenAt          *time.Time `json:"last_seen_at"`
	RevokedAt           *time.Time `json:"revoked_at"`
}

// Actor é quem age no tenant. Tokens de API não saem: são segredos da
// instalação, e quem importa emite novos.
type Actor struct {
	ID         uuid.UUID  `json:"id"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at"`
}

// Snapshot são os metadados de um snapshot; os bytes estão em Body.
type Snapshot struct {
	ID            uuid.UUID `json:"id"`
	AssetID       uuid.UUID `json:"asset_id"`
	AgentID       uuid.UUID `json:"agent_id"`
	SchemaVersion string    `json:"schema_version"`
	CollectedAt   time.Time `json:"collected_at"`
	ReceivedAt    time.Time `json:"received_at"`
	SHA256        string    `json:"sha256"`
	PrevSHA256    string    `json:"prev_sha256,omitempty"`
	// Body é o caminho dos bytes originais no pacote, snapshots/<sha256>.json.
	// Vazio nos snapshots recebidos antes de o servidor guardar os bytes: aí
	// Payload aponta para snapshots/<sha256>.payload.json, o mesmo conteúdo
	// reescrito pelo banco, que serve para recalcular o estado mas não para
	// reconferir o sha256.
	Body    string `json:"body,omitempty"`
	Payload string `json:"payload,omitempty"`
}

// Product é um produto criado à mão pelo tenant. VendorScope diz se o
// fabricante é do catálogo comum ("catalog") ou também do tenant ("tenant").
type Product struct {
	ID          uuid.UUID  `json:"id"`
	VendorID    *uuid.UUID `json:"vendor_id"`
	Vendor      *string    `json:"vendor"`
	VendorScope string     `json:"vendor_scope,omitempty"`
	Product     string     `json:"product"`
	CPEVendor   *string    `json:"cpe_vendor"`
	CPEProduct  *string    `json:"cpe_product"`
}

// Mapping é um mapeamento manual do tenant. O nome bruto vai por extenso;
// o produto, por id e por nome, com ProductScope "catalog" (procurar pelo
// nome no catálogo de quem importa) ou "tenant" (está em products.jsonl).
type Mapping struct {
	Source       string     `json:"source"`
	RawName      string     `json:"raw_name"`
	RawVendor    string     `json:"raw_vendor"`
	SoftwareID   uuid.UUID  `json:"software_id"`
	Vendor       *string    `json:"vendor"`
	Product      string     `json:"product"`
	ProductScope string     `json:"product_scope"`
	MappedBy     *uuid.UUID `json:"mapped_by"`
	MappedAt     time.Time  `json:"mapped_at"`
}

// Proposal é uma proposta de agente de IA, com a decisão humana.
type Proposal struct {
	ID         uuid.UUID       `json:"id"`
	ActorID    uuid.UUID       `json:"actor_id"`
	Kind       string          `json:"kind"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Payload    json.RawMessage `json:"payload"`
	Evidence   string          `json:"evidence"`
	Confidence *float32        `json:"confidence"`
	Status     string          `json:"status"`
	ReviewedBy *uuid.UUID      `json:"reviewed_by"`
	ReviewedAt *time.Time      `json:"reviewed_at"`
	ReviewNote *string         `json:"review_note"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Change é um evento do change_log: o histórico do tenant como foi
// registrado. Ids de linhas derivadas (installation_id) referem-se à
// instalação de origem e não sobrevivem ao recálculo.
type Change struct {
	ID         int64           `json:"id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Kind       string          `json:"kind"`
	AssetID    *uuid.UUID      `json:"asset_id"`
	ActorID    *uuid.UUID      `json:"actor_id"`
	Data       json.RawMessage `json:"data"`
}
