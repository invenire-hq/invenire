// SPDX-License-Identifier: AGPL-3.0-only

// Package download distribui o agente pelo próprio servidor: os executáveis
// de uma pasta (na imagem Docker, compilados junto com o servidor), o
// SHA256SUMS deles, os scripts de instalação com o endereço já preenchido e o
// pacote pré-configurado que o painel monta para um token de cadastro.
//
// O executável e os scripts não são segredo: as rotas são públicas. O pacote
// leva o token e só sai pelo painel, para um administrador.
package download

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDir é onde a imagem Docker guarda os executáveis do agente.
const DefaultDir = "/usr/local/share/invenire/agent"

// Platform é um sistema e uma arquitetura para os quais há agente.
type Platform struct{ OS, Arch string }

func (p Platform) String() string { return p.OS + "-" + p.Arch }

// FileName é o nome do executável na pasta e no SHA256SUMS, o mesmo que o
// scripts/build.sh gera.
func (p Platform) FileName() string {
	n := "invenire-agent-" + p.OS + "-" + p.Arch
	if p.OS == "windows" {
		n += ".exe"
	}
	return n
}

// Platforms são as plataformas que o servidor distribui.
var Platforms = []Platform{{"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}}

// Agents é a pasta com os executáveis do agente e o SHA256SUMS.
type Agents struct {
	dir     string
	sums    []byte            // o SHA256SUMS como está na pasta
	files   map[string]string // plataforma -> SHA-256 conferido
	problem string            // por que não há agente nenhum, para o painel
	missing []string          // plataformas sem executável, para o log
}

// Open lê a pasta e confere cada executável contra o SHA256SUMS. Nunca
// falha: sem executáveis o servidor funciona, e o painel explica o que falta
// (Problem).
func Open(dir string) *Agents {
	a := &Agents{dir: dir, files: map[string]string{}}
	if dir == "" {
		a.problem = "nenhuma pasta de executáveis do agente foi configurada (--agent-dir)"
		return a
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		a.problem = fmt.Sprintf("a pasta %s não tem os executáveis do agente (falta o SHA256SUMS)", dir)
		return a
	}
	a.sums = sums
	want := parseSums(sums)
	for _, p := range Platforms {
		h, ok := want[p.FileName()]
		if !ok {
			a.missing = append(a.missing, p.String()+": fora do SHA256SUMS")
			continue
		}
		got, err := fileSHA256(filepath.Join(dir, p.FileName()))
		switch {
		case err != nil:
			a.missing = append(a.missing, p.String()+": "+err.Error())
		case got != h:
			a.missing = append(a.missing, p.String()+": SHA-256 não confere com o SHA256SUMS")
		default:
			a.files[p.String()] = got
		}
	}
	if len(a.files) == 0 {
		a.problem = fmt.Sprintf("nenhum executável do agente em %s confere com o SHA256SUMS da pasta", dir)
	}
	return a
}

// Dir é a pasta lida.
func (a *Agents) Dir() string { return a.dir }

// Problem diz por que não há agente para distribuir; vazio quando há.
func (a *Agents) Problem() string { return a.problem }

// Missing lista as plataformas que ficaram de fora e o motivo.
func (a *Agents) Missing() []string { return a.missing }

// Has diz se há executável conferido para a plataforma ("linux-amd64").
func (a *Agents) Has(platform string) bool { _, ok := a.files[platform]; return ok }

func parseSums(b []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			m[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return m
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("executável não encontrado")
		}
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readAgent lê o executável conferindo de novo o SHA-256: entre a subida e o
// pacote, alguém pode ter trocado o arquivo na pasta.
func (a *Agents) readAgent(p Platform) ([]byte, error) {
	want, ok := a.files[p.String()]
	if !ok {
		return nil, fmt.Errorf("não há agente para %s", p)
	}
	b, err := os.ReadFile(filepath.Join(a.dir, p.FileName()))
	if err != nil {
		return nil, err
	}
	if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != want {
		return nil, fmt.Errorf("%s mudou na pasta depois que o servidor subiu: reinicie o servidor", p.FileName())
	}
	return b, nil
}

// ServeAgent é GET /download/agent/{plataforma}.
func (a *Agents) ServeAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("platform")
	var p Platform
	for _, q := range Platforms {
		if q.String() == name {
			p = q
		}
	}
	if !a.Has(name) {
		http.Error(w, "não há agente para "+name+" neste servidor", http.StatusNotFound)
		return
	}
	f, err := os.Open(filepath.Join(a.dir, p.FileName()))
	if err != nil {
		http.Error(w, "agente indisponível", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "agente indisponível", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.FileName()+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Checksum-Sha256", a.files[name])
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// ServeSums é GET /download/SHA256SUMS, o arquivo da pasta como está.
func (a *Agents) ServeSums(w http.ResponseWriter, r *http.Request) {
	if a.sums == nil {
		http.Error(w, "não há agente neste servidor", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(a.sums)
}
