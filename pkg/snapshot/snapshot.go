// Package snapshot define o formato do inventário que o agente envia e a API
// recebe. É o contrato entre os dois binários: toda mudança incompatível
// exige um novo SchemaVersion.
package snapshot

//go:generate go run ./internal/schemagen -out schema/v1.json

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	_ "embed"
)

// SchemaVersion é a versão do formato produzida por este pacote.
const SchemaVersion = "1"

// SchemaV1 é o JSON Schema gerado a partir das structs deste pacote.
//
//go:embed schema/v1.json
var SchemaV1 []byte

// Snapshot é um inventário completo de um ativo num instante.
type Snapshot struct {
	SchemaVersion string    `json:"schema_version"`
	AgentID       string    `json:"agent_id,omitempty" jsonschema:"description=Vazio no modo offline, antes do enrollment"`
	AgentVersion  string    `json:"agent_version"`
	CollectedAt   time.Time `json:"collected_at"`
	// PrevSHA256 é o SHA-256 (hex) do snapshot anterior do mesmo ativo.
	// Vazio no primeiro snapshot e no modo offline.
	PrevSHA256 string `json:"prev_sha256,omitempty" jsonschema:"pattern=^[0-9a-f]{64}$"`

	Host              Host               `json:"host"`
	OS                OS                 `json:"os"`
	Hardware          Hardware           `json:"hardware"`
	Software          []Software         `json:"software"`
	BrowserExtensions []BrowserExtension `json:"browser_extensions"`
	Users             []User             `json:"users"`
	Network           Network            `json:"network"`

	// CollectorErrors registra coletores que falharam. A falha de um coletor
	// não impede o envio do snapshot.
	CollectorErrors []CollectorError `json:"collector_errors,omitempty"`
}

// Host identifica a máquina. Hostname é atributo, nunca chave; a identidade
// do ativo vem de Fingerprint.
type Host struct {
	Hostname     string `json:"hostname"`
	SMBIOSUUID   string `json:"smbios_uuid,omitempty"`
	MachineID    string `json:"machine_id,omitempty" jsonschema:"description=MachineGuid no Windows, /etc/machine-id no Linux"`
	Serial       string `json:"serial,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
}

type OS struct {
	Family  string `json:"family" jsonschema:"enum=windows,enum=linux,enum=other"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Build   string `json:"build,omitempty"`
	Kernel  string `json:"kernel,omitempty"`
	Arch    string `json:"arch,omitempty"`
	// ID é o identificador da distribuição (ID do os-release: debian, ubuntu,
	// rhel…). Necessário para montar o PURL dos pacotes Linux.
	ID string `json:"id,omitempty"`
}

type Hardware struct {
	CPUModel    string `json:"cpu_model,omitempty"`
	CPUCores    int    `json:"cpu_cores,omitempty"`
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
	Disks       []Disk `json:"disks,omitempty"`
}

type Disk struct {
	Name      string `json:"name"`
	Model     string `json:"model,omitempty"`
	Serial    string `json:"serial,omitempty"`
	SizeBytes uint64 `json:"size_bytes,omitempty"`
}

// Fontes de software conhecidas.
const (
	SourceWinRegistry = "win_registry"
	SourceDpkg        = "dpkg"
	SourceRpm         = "rpm"
)

// Software é um programa ou pacote exatamente como o sistema o reporta.
// A normalização é feita pela API, nunca pelo agente.
type Software struct {
	Source string `json:"source" jsonschema:"enum=win_registry,enum=dpkg,enum=rpm"`
	// PackageID é o identificador do pacote na fonte: nome do pacote no
	// dpkg/rpm, nome da subchave Uninstall no Windows (o ProductCode, no MSI).
	PackageID   string `json:"package_id,omitempty"`
	Name        string `json:"name"`
	Publisher   string `json:"publisher,omitempty"`
	Version     string `json:"version,omitempty"`
	Arch        string `json:"arch,omitempty"`
	InstallPath string `json:"install_path,omitempty"`
	InstallDate string `json:"install_date,omitempty" jsonschema:"description=Como a fonte reporta (YYYYMMDD no registro, RFC 3339 no rpm)"`
	// UserSID é preenchido quando a instalação é por usuário (HKU).
	UserSID         string `json:"user_sid,omitempty"`
	SystemComponent bool   `json:"system_component,omitempty"`
}

type BrowserExtension struct {
	Browser     string   `json:"browser" jsonschema:"enum=chrome,enum=edge,enum=firefox"`
	Profile     string   `json:"profile,omitempty"`
	User        string   `json:"user,omitempty"`
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Version     string   `json:"version,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

type User struct {
	Name      string     `json:"name"`
	SID       string     `json:"sid,omitempty"`
	UID       string     `json:"uid,omitempty"`
	Admin     bool       `json:"admin,omitempty"`
	Disabled  bool       `json:"disabled,omitempty"`
	LastLogon *time.Time `json:"last_logon,omitempty"`
}

type Network struct {
	Interfaces []Interface `json:"interfaces"`
	Listeners  []Listener  `json:"listeners"`
}

type Interface struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac,omitempty"`
	Addrs []string `json:"addrs,omitempty" jsonschema:"description=Endereços em notação CIDR"`
}

type Listener struct {
	Protocol string `json:"protocol" jsonschema:"enum=tcp,enum=udp"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	PID      int    `json:"pid,omitempty"`
	Process  string `json:"process,omitempty"`
}

type CollectorError struct {
	Collector string `json:"collector"`
	Error     string `json:"error"`
}

// New devolve um snapshot com a versão do schema e as listas inicializadas.
func New(agentVersion string, collectedAt time.Time) *Snapshot {
	s := &Snapshot{
		SchemaVersion: SchemaVersion,
		AgentVersion:  agentVersion,
		CollectedAt:   collectedAt,
	}
	s.fillNil()
	return s
}

// fillNil troca listas nulas por vazias, para o JSON trazer [] e não null.
func (s *Snapshot) fillNil() {
	if s.Software == nil {
		s.Software = []Software{}
	}
	if s.BrowserExtensions == nil {
		s.BrowserExtensions = []BrowserExtension{}
	}
	if s.Users == nil {
		s.Users = []User{}
	}
	if s.Network.Interfaces == nil {
		s.Network.Interfaces = []Interface{}
	}
	if s.Network.Listeners == nil {
		s.Network.Listeners = []Listener{}
	}
}

// Marshal serializa o snapshot em JSON compacto e devolve o SHA-256 desses
// bytes. O hash é sempre calculado sobre o corpo exatamente como enviado:
// é ele que a API confere e que o próximo snapshot leva em PrevSHA256.
func Marshal(s *Snapshot) (body []byte, sum string, err error) {
	if err := s.Validate(); err != nil {
		return nil, "", err
	}
	s.fillNil()
	body, err = json.Marshal(s)
	if err != nil {
		return nil, "", err
	}
	return body, Sum(body), nil
}

// Sum devolve o SHA-256 em hex de um corpo de snapshot.
func Sum(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Validate faz as checagens que não dependem do JSON Schema.
func (s *Snapshot) Validate() error {
	var errs []error
	if s.SchemaVersion != SchemaVersion {
		errs = append(errs, errors.New("schema_version não suportada: "+s.SchemaVersion))
	}
	if s.CollectedAt.IsZero() {
		errs = append(errs, errors.New("collected_at vazio"))
	}
	if Fingerprint(s.Host) == "" {
		errs = append(errs, errors.New("host sem smbios_uuid nem machine_id: ativo não identificável"))
	}
	return errors.Join(errs...)
}
