// Package agente embute os scripts de instalação do agente no servidor, que
// os entrega no pacote pré-configurado e no comando de uma linha (como o
// pacote migrations faz com as migrações).
package agente

import (
	_ "embed"
	"strings"
)

var (
	//go:embed instalar.ps1
	ps1 string
	//go:embed instalar.sh
	sh string
	//go:embed Instalar.cmd
	cmd string
)

// Os scripts têm o fim de linha do sistema em que rodam, qualquer que seja o
// do checkout: CRLF no Windows (o cmd.exe se perde com LF), LF no Linux.
func lf(s string) string   { return strings.ReplaceAll(s, "\r\n", "\n") }
func crlf(s string) string { return strings.ReplaceAll(lf(s), "\n", "\r\n") }

// PS1 é o instalar.ps1, em UTF-8 com BOM (o Windows PowerShell 5.1 lê sem BOM
// como ANSI e estraga os acentos).
func PS1() string { return bom + strings.TrimPrefix(crlf(ps1), bom) }

const bom = "\uFEFF"

// SH é o instalar.sh.
func SH() string { return lf(sh) }

// CMD é o Instalar.cmd, que pede elevação e chama o instalar.ps1.
func CMD() string { return crlf(cmd) }

// As linhas que o servidor preenche no script que ele mesmo serve (o do
// comando de uma linha): endereço e pin.
const (
	servedServerPS1 = `$ServedServer = ""`
	servedPinPS1    = `$ServedPin = ""`
	servedServerSH  = `served_server=""`
	servedPinSH     = `served_pin=""`
)

// ServedPS1 é o instalar.ps1 com endereço e pin já preenchidos, sem BOM (vai
// pela rede, decodificado pelo Invoke-RestMethod). Os valores precisam vir
// conferidos: entram entre aspas simples, sem escape.
func ServedPS1(server, pin string) string {
	s := strings.TrimPrefix(crlf(ps1), bom)
	s = strings.Replace(s, servedServerPS1, `$ServedServer = '`+server+`'`, 1)
	return strings.Replace(s, servedPinPS1, `$ServedPin = '`+pin+`'`, 1)
}

// ServedSH é o instalar.sh com endereço e pin preenchidos (mesma regra).
func ServedSH(server, pin string) string {
	s := SH()
	s = strings.Replace(s, servedServerSH, `served_server='`+server+`'`, 1)
	return strings.Replace(s, servedPinSH, `served_pin='`+pin+`'`, 1)
}
