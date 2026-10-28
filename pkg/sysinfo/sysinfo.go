// Package sysinfo lê o estado de baixo nível da máquina local: identificação
// do hardware (SMBIOS), machine-id, sistema operacional e software instalado.
//
// O pacote só lê, nunca altera o sistema, e não conhece nenhum formato de
// saída: devolve os dados como o sistema os reporta e informa de onde cada um
// veio. O que fazer com falhas, hashes e saída é decisão de quem chama — o
// agente do Invenire ignora a falha e segue; uma coleta forense a documenta.
// Por isso o pacote depende apenas de golang.org/x/sys, para poder virar um
// módulo próprio, compartilhado com o PeritiaGo.
//
// As funções devolvem resultados parciais junto com o erro quando parte da
// leitura falha.
package sysinfo

import "errors"

// ErrUnsupported indica leitura não implementada nesta plataforma.
var ErrUnsupported = errors.New("sysinfo: não suportado nesta plataforma")

// System é a identificação do hardware, da estrutura SMBIOS tipo 1.
type System struct {
	Manufacturer string
	Product      string
	Version      string
	Serial       string
	// UUID em maiúsculas, como o SMBIOS o formata. Pode ser um valor-lixo
	// gravado pelo fabricante (zeros, FF…); quem chama decide se o aceita.
	UUID string
}

// OS descreve o sistema operacional.
type OS struct {
	Family  string // windows, linux
	Name    string
	Version string // DisplayVersion no Windows (23H2), VERSION_ID no Linux
	Build   string
	Kernel  string
	Arch    string
	ID      string // ID do os-release (debian, ubuntu, rhel…); vazio no Windows
}

// Fontes de pacotes.
const (
	SourceWinRegistry = "win_registry"
	SourceDpkg        = "dpkg"
	SourceRpm         = "rpm"
)

// Package é um programa ou pacote instalado, exatamente como a fonte o
// reporta, sem normalização.
type Package struct {
	Source string
	// ID é o identificador na fonte: nome do pacote no dpkg/rpm, nome da
	// subchave Uninstall no Windows (o ProductCode, no MSI).
	ID          string
	Name        string
	Publisher   string
	Version     string
	Arch        string
	InstallPath string
	// InstallDate como a fonte reporta: YYYYMMDD no registro, RFC 3339 no rpm.
	InstallDate string
	// UserSID é preenchido quando a instalação é por usuário (HKU).
	UserSID         string
	SystemComponent bool
	// Location diz de onde o registro foi lido: o caminho completo da chave
	// no registro, o arquivo de status do dpkg ou o comando rpm.
	Location string
}
