// SPDX-License-Identifier: AGPL-3.0-only

package download

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/invenire-hq/invenire/deploy/agente"
)

// Config é o que vai no instalar.conf do pacote.
type Config struct {
	Server    string // https://host[:porta]
	Pin       string // sha256:..., vazio com certificado público
	Token     string
	ExpiresAt time.Time
	// Remaining é quantas máquinas o token ainda cadastra. Com uma só, o
	// script apaga o instalar.conf depois de instalar; com mais, o mesmo
	// pacote serve a outras máquinas, e o script só avisa.
	Remaining int
	Generated time.Time
}

var (
	// O endereço entra no script e no instalar.conf entre aspas simples, sem
	// escape: só passa o que um host[:porta] pode ter.
	serverRe = regexp.MustCompile(`^https://(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)
	pinRe    = regexp.MustCompile(`^(sha256:[0-9a-f]{64})?$`)
	tokenRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
)

// ValidServer diz se o endereço pode ir num script ou no instalar.conf.
func ValidServer(s string) bool { return serverRe.MatchString(s) }

func (c Config) validate() error {
	switch {
	case !ValidServer(c.Server):
		return fmt.Errorf("endereço do servidor inválido para o pacote: %q", c.Server)
	case !pinRe.MatchString(c.Pin):
		return errors.New("pin inválido para o pacote")
	case !tokenRe.MatchString(c.Token):
		return errors.New("token inválido para o pacote")
	}
	return nil
}

func (c Config) conf(eol string) []byte {
	del := "nao"
	if c.Remaining <= 1 {
		del = "sim"
	}
	lines := []string{
		"# Configuração do instalador do Invenire, gerada pelo painel em " + c.Generated.Format("02/01/2006 15:04") + ".",
		"#",
		"# ATENÇÃO: este arquivo tem um token de cadastro. Quem tiver este arquivo",
		"# cadastra máquinas no seu Invenire até " + c.ExpiresAt.Format("02/01/2006 15:04") + " ou até acabarem os usos.",
		"# Se o pacote se perder ou vazar, revogue o token no painel (Adicionar máquinas).",
		"# apagar_depois=sim: o instalador apaga este arquivo depois de instalar.",
		"servidor=" + c.Server,
		"ca_pin=" + c.Pin,
		"token=" + c.Token,
		"apagar_depois=" + del,
	}
	return []byte(strings.Join(lines, eol) + eol)
}

func (c Config) readme(osName string) string {
	var b strings.Builder
	b.WriteString("Invenire: instalador do agente\n\n")
	if osName == "windows" {
		b.WriteString("1. Extraia o .zip inteiro (botão direito > Extrair tudo).\n" +
			"2. Na pasta extraída, dê dois cliques em Instalar.cmd e responda \"Sim\" na\n" +
			"   janela que pede permissão de administrador.\n" +
			"3. Espere a mensagem \"pronto\". A máquina aparece no painel em alguns minutos.\n\n" +
			"Se o Windows mostrar \"O Windows protegeu o computador\" (SmartScreen), clique em\n" +
			"\"Mais informações\" e depois em \"Executar assim mesmo\": o programa não tem\n" +
			"assinatura de código. O instalador confere o executável contra o SHA256SUMS\n" +
			"antes de rodar. Se um antivírus bloquear o invenire-agent.exe, peça a quem cuida\n" +
			"dele para liberar o arquivo.\n\n")
	} else {
		b.WriteString("Na pasta onde o pacote foi baixado:\n\n" +
			"    tar xzf invenire-agente-linux.tar.gz\n" +
			"    sudo ./invenire-agente/instalar.sh\n\n" +
			"A máquina aparece no painel em alguns minutos. Precisa de systemd.\n\n")
	}
	b.WriteString("Rodar de novo não cadastra outra vez: só atualiza o serviço.\n\n" +
		"O arquivo instalar.conf tem o token de cadastro, válido até " + c.ExpiresAt.Format("02/01/2006 15:04") + ".\n")
	if c.Remaining <= 1 {
		b.WriteString("O instalador o apaga depois de instalar.\n")
	} else {
		b.WriteString(fmt.Sprintf("Ele ainda cadastra %d máquinas: guarde o pacote em lugar seguro e apague-o\nquando terminar. Se ele se perder, revogue o token no painel.\n", c.Remaining))
	}
	return b.String()
}

type entry struct {
	name string
	mode int64
	data []byte
}

// Package monta o pacote do instalador: .zip para "windows" (com o
// Instalar.cmd de dois cliques), .tar.gz para "linux" (com os agentes amd64
// e arm64 que houver). Tudo dentro de uma pasta invenire-agente/.
func (a *Agents) Package(osName string, c Config) (filename, contentType string, body []byte, err error) {
	if err := c.validate(); err != nil {
		return "", "", nil, err
	}
	var files []entry
	switch osName {
	case "windows":
		bin, err := a.readAgent(Platform{"windows", "amd64"})
		if err != nil {
			return "", "", nil, err
		}
		files = []entry{
			{"Instalar.cmd", 0o644, []byte(agente.CMD())},
			{"instalar.ps1", 0o644, []byte(agente.PS1())},
			{"invenire-agent.exe", 0o755, bin},
		}
	case "linux":
		files = []entry{{"instalar.sh", 0o755, []byte(agente.SH())}}
		for _, p := range Platforms {
			if p.OS != "linux" || !a.Has(p.String()) {
				continue
			}
			bin, err := a.readAgent(p)
			if err != nil {
				return "", "", nil, err
			}
			files = append(files, entry{p.FileName(), 0o755, bin})
		}
		if len(files) == 1 {
			return "", "", nil, errors.New("não há agente para Linux")
		}
	default:
		return "", "", nil, fmt.Errorf("sistema desconhecido: %q", osName)
	}
	var sums strings.Builder
	for _, f := range files {
		h := sha256.Sum256(f.data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), f.name)
	}
	eol := "\n"
	if osName == "windows" {
		eol = "\r\n"
	}
	files = append(files,
		entry{"SHA256SUMS", 0o644, []byte(sums.String())},
		entry{"LEIA-ME.txt", 0o644, []byte(strings.ReplaceAll(c.readme(osName), "\n", eol))},
		entry{"instalar.conf", 0o600, c.conf(eol)},
	)

	const dir = "invenire-agente/"
	var buf bytes.Buffer
	if osName == "windows" {
		zw := zip.NewWriter(&buf)
		for _, f := range files {
			h := &zip.FileHeader{Name: dir + f.name, Method: zip.Deflate, Modified: c.Generated}
			h.SetMode(0o644)
			w, err := zw.CreateHeader(h)
			if err != nil {
				return "", "", nil, err
			}
			w.Write(f.data)
		}
		if err := zw.Close(); err != nil {
			return "", "", nil, err
		}
		return "invenire-agente-windows.zip", "application/zip", buf.Bytes(), nil
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir, Mode: 0o755, ModTime: c.Generated})
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: dir + f.name, Mode: f.mode, Size: int64(len(f.data)), ModTime: c.Generated}); err != nil {
			return "", "", nil, err
		}
		tw.Write(f.data)
	}
	if err := tw.Close(); err != nil {
		return "", "", nil, err
	}
	if err := gz.Close(); err != nil {
		return "", "", nil, err
	}
	return "invenire-agente-linux.tar.gz", "application/gzip", buf.Bytes(), nil
}

// ServeScript é GET /download/instalar.ps1 e /download/instalar.sh: o script
// do comando de uma linha, com endereço e pin já preenchidos. Sem token: ele
// vem da variável de ambiente de quem roda.
func ServeScript(w http.ResponseWriter, kind, server, pin string) {
	if !ValidServer(server) || !pinRe.MatchString(pin) {
		http.Error(w, "endereço do servidor inválido", http.StatusBadRequest)
		return
	}
	var body string
	switch kind {
	case "ps1":
		body = agente.ServedPS1(server, pin)
	case "sh":
		body = agente.ServedSH(server, pin)
	default:
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(body))
}
