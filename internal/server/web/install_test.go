package web_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/download"
	"github.com/athosbes/invenire/internal/server/store"
)

// fakeAgents é uma pasta como a do scripts/build.sh, com executáveis de mentira.
func fakeAgents(t *testing.T) *download.Agents {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	for _, name := range []string{"invenire-agent-windows-amd64.exe", "invenire-agent-linux-amd64", "invenire-agent-linux-arm64"} {
		data := []byte("executável de mentira: " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644)
	a := download.Open(dir)
	if a.Problem() != "" {
		t.Fatal(a.Problem())
	}
	return a
}

var tokenRe = regexp.MustCompile(`inv_[A-Za-z0-9_-]{43}`)

// newToken cria um token pelo painel e devolve o token e a página.
func (b *browser) newToken(uses string) (string, string) {
	b.e.t.Helper()
	b.get("/enroll")
	st, body, _ := b.post("/enroll", url.Values{"uses": {uses}, "ttl": {"24h"}})
	if st != http.StatusOK {
		b.e.t.Fatalf("criar token: %d", st)
	}
	tok := tokenRe.FindString(body)
	if tok == "" {
		b.e.t.Fatal("a página não mostrou o token")
	}
	return tok, body
}

func (b *browser) pkg(token, system string) (int, []byte, http.Header) {
	st, body, h := b.post("/enroll/package", url.Values{"token": {token}, "so": {system}})
	return st, []byte(body), h
}

func zipConf(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("não é zip: %v", err)
	}
	var conf string
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "invenire-agente/instalar.conf" {
			rc, _ := f.Open()
			c, _ := io.ReadAll(rc)
			rc.Close()
			conf = string(c)
		}
	}
	for _, n := range []string{"Instalar.cmd", "instalar.ps1", "invenire-agent.exe", "SHA256SUMS", "instalar.conf"} {
		if !names["invenire-agente/"+n] {
			t.Fatalf("falta %s no zip", n)
		}
	}
	return conf
}

func tarConf(t *testing.T, b []byte) string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("não é gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	var conf string
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
		if h.Name == "invenire-agente/instalar.conf" {
			c, _ := io.ReadAll(tr)
			conf = string(c)
		}
	}
	for _, n := range []string{"instalar.sh", "invenire-agent-linux-amd64", "invenire-agent-linux-arm64", "SHA256SUMS", "instalar.conf"} {
		if !names["invenire-agente/"+n] {
			t.Fatalf("falta %s no tar.gz", n)
		}
	}
	return conf
}

// noTokenInURLs: o token de cadastro nunca vai numa URL, nem nas pedidas ao
// servidor nem nas que a página mostra.
func (e *env) noTokenInURLs(token, page string) {
	e.t.Helper()
	e.urlsMu.Lock()
	defer e.urlsMu.Unlock()
	for _, u := range e.urls {
		if strings.Contains(u, token) {
			e.t.Fatalf("o token apareceu na URL %s", u)
		}
	}
	for _, u := range regexp.MustCompile(`(https?://|/)[^\s"'<>]*`).FindAllString(page, -1) {
		if strings.Contains(u, token) {
			e.t.Fatalf("o token apareceu numa URL da página: %s", u)
		}
	}
	for _, attr := range regexp.MustCompile(`(?:href|action|src)="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		if strings.Contains(attr[1], token) {
			e.t.Fatalf("o token apareceu em %s", attr[0])
		}
	}
}

func TestInstallerPackagePin(t *testing.T) {
	e := newEnvWith(t, envOpts{agents: fakeAgents(t)})
	o := e.org("Org", "admin@exemplo.org")
	b := e.browser()
	b.login("admin@exemplo.org", pw)

	tok, page := b.newToken("1")
	if !strings.Contains(page, `action="/enroll/package"`) || !strings.Contains(page, "Baixar pacote para Windows") || !strings.Contains(page, "Baixar pacote para Linux") {
		t.Fatal("a tela do token deveria oferecer os pacotes")
	}
	if strings.Contains(page, "irm ") || strings.Contains(page, "curl -fsSL") || !strings.Contains(page, "Não disponível neste servidor") {
		t.Fatal("com pin, a tela não deveria oferecer o comando de uma linha")
	}
	if !strings.Contains(page, "SmartScreen") || !strings.Contains(page, "O pacote contém o token") {
		t.Fatal("faltam os avisos do pacote (token dentro, SmartScreen)")
	}
	pin := e.api.CAPin()

	st, body, h := b.pkg(tok, "windows")
	if st != http.StatusOK || h.Get("Content-Type") != "application/zip" || !strings.Contains(h.Get("Content-Disposition"), "invenire-agente-windows.zip") {
		t.Fatalf("pacote Windows: %d %s %s", st, h.Get("Content-Type"), h.Get("Content-Disposition"))
	}
	conf := zipConf(t, body)
	for _, want := range []string{"servidor=" + e.srv.URL + "\r\n", "ca_pin=" + pin + "\r\n", "token=" + tok + "\r\n", "apagar_depois=sim\r\n"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("instalar.conf do zip sem %q:\n%s", want, conf)
		}
	}
	st, body, h = b.pkg(tok, "linux")
	if st != http.StatusOK || h.Get("Content-Type") != "application/gzip" {
		t.Fatalf("pacote Linux: %d %s", st, h.Get("Content-Type"))
	}
	conf = tarConf(t, body)
	for _, want := range []string{"servidor=" + e.srv.URL + "\n", "ca_pin=" + pin + "\n", "token=" + tok + "\n"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("instalar.conf do tar.gz sem %q:\n%s", want, conf)
		}
	}
	var n int
	e.pool.QueryRow(context.Background(), "SELECT count(*) FROM change_log WHERE tenant_id = $1 AND kind = 'enrollment_package_downloaded' AND actor_id = $2",
		o.Tenant.ID, o.Admin.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("%d pacotes no change_log, esperado 2", n)
	}

	// Sistema desconhecido.
	if st, _, _ := b.pkg(tok, "macos"); st != http.StatusBadRequest {
		t.Fatalf("sistema desconhecido: %d", st)
	}
	// Token que não existe, ou revogado: sem pacote.
	if st, _, _ := b.pkg("inv_"+strings.Repeat("A", 43), "windows"); st != http.StatusBadRequest {
		t.Fatalf("token inventado: %d", st)
	}
	tok2, _ := b.newToken("3")
	st, body, _ = b.pkg(tok2, "windows")
	if st != http.StatusOK || !strings.Contains(zipConf(t, body), "apagar_depois=nao") {
		t.Fatal("token de várias máquinas: o instalador não deveria apagar a configuração")
	}
	var id string
	e.pool.QueryRow(context.Background(), "SELECT id FROM enrollment_token WHERE token_hash = sha256($1::bytea)", []byte(tok2)).Scan(&id)
	b.get("/enroll")
	if st, _, _ := b.post("/enroll/"+id+"/revoke", nil); st != http.StatusSeeOther {
		t.Fatalf("revogar: %d", st)
	}
	if st, body, _ := b.pkg(tok2, "windows"); st != http.StatusBadRequest || !strings.Contains(string(body), "não vale mais") {
		t.Fatalf("token revogado: %d", st)
	}

	// Rotas públicas, sem sessão.
	anon := e.browser()
	for path, want := range map[string]string{
		"/download/agent/windows-amd64": "executável de mentira: invenire-agent-windows-amd64.exe",
		"/download/agent/linux-arm64":   "executável de mentira: invenire-agent-linux-arm64",
		"/download/SHA256SUMS":          "  invenire-agent-linux-amd64\n",
		"/download/instalar.ps1":        "$ServedServer = '" + e.srv.URL + "'",
		"/download/instalar.sh":         "served_pin='" + pin + "'",
	} {
		if st, body := anon.get(path); st != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("%s: %d, sem %q", path, st, want)
		}
	}
	if st, _ := anon.get("/download/agent/windows-arm64"); st != http.StatusNotFound {
		t.Fatalf("plataforma sem agente: %d", st)
	}
	e.noTokenInURLs(tok, page)
	e.noTokenInURLs(tok2, page)
}

func TestInstallerPackagePublic(t *testing.T) {
	e := newEnvWith(t, envOpts{public: true, agents: fakeAgents(t)})
	e.org("Org", "admin@exemplo.org")
	b := e.browser()
	b.login("admin@exemplo.org", pw)

	tok, page := b.newToken("1")
	ps := "&amp; ([scriptblock]::Create((irm " + e.srv.URL + "/download/instalar.ps1)))"
	sh := "curl -fsSL " + e.srv.URL + "/download/instalar.sh | sudo INVENIRE_ENROLL_TOKEN=" + tok + " sh"
	if !strings.Contains(page, `$env:INVENIRE_ENROLL_TOKEN = "`+tok+`"`) || !strings.Contains(page, ps) || !strings.Contains(page, sh) {
		t.Fatalf("com certificado público, a tela deveria mostrar o comando de uma linha:\n%s", page)
	}
	st, body, _ := b.pkg(tok, "windows")
	if st != http.StatusOK {
		t.Fatalf("pacote: %d", st)
	}
	conf := zipConf(t, body)
	if !strings.Contains(conf, "servidor="+e.srv.URL+"\r\n") || !strings.Contains(conf, "ca_pin=\r\n") || !strings.Contains(conf, "token="+tok) {
		t.Fatalf("instalar.conf com certificado público:\n%s", conf)
	}
	st, body, _ = b.pkg(tok, "linux")
	if conf := tarConf(t, body); st != http.StatusOK || !strings.Contains(conf, "ca_pin=\n") {
		t.Fatalf("instalar.conf do Linux com certificado público:\n%s", conf)
	}
	if _, body := e.browser().get("/download/instalar.sh"); !strings.Contains(body, "served_server='"+e.srv.URL+"'") || !strings.Contains(body, "served_pin=''") {
		t.Fatal("o script servido deveria trazer o endereço e nenhum pin")
	}
	e.noTokenInURLs(tok, page)
}

func TestInstallerPackageRefused(t *testing.T) {
	e := newEnvWith(t, envOpts{agents: fakeAgents(t)})
	ctx := context.Background()
	o := e.org("Org", "admin@exemplo.org")
	e.org("Outra", "admin@outra.org")
	if _, err := e.api.CreatePanelUser(ctx, o.Admin, api.NewPanelUser{Name: "Leitor", Email: "leitor@exemplo.org", Password: pw}); err != nil {
		t.Fatal(err)
	}
	var tok string
	e.api.InTx(ctx, func(q *store.Queries) error {
		nt, err := api.IssueEnrollmentToken(ctx, q, &o.Admin.ID, api.TokenSpec{Tenant: o.Tenant.ID, TTL: time.Hour, Uses: 1})
		tok = nt.Token
		return err
	})
	form := url.Values{"token": {tok}, "so": {"windows"}}

	// Sem sessão: vai para o login, sem pacote.
	anon := e.browser()
	if st, body, h := anon.do("POST", "/enroll/package", form); st != http.StatusSeeOther || h.Get("Location") != "/login" || strings.Contains(body, "PK") {
		t.Fatalf("sem sessão: %d %s", st, h.Get("Location"))
	}
	// Usuário só leitura: proibido.
	r := e.browser()
	r.login("leitor@exemplo.org", pw)
	if st, _, h := r.pkg(tok, "windows"); st != http.StatusForbidden || h.Get("Content-Type") == "application/zip" {
		t.Fatalf("leitor: %d", st)
	}
	// Administrador de outra organização: o token não é dela.
	other := e.browser()
	other.login("admin@outra.org", pw)
	if st, _, _ := other.pkg(tok, "windows"); st != http.StatusBadRequest {
		t.Fatalf("outra organização: %d", st)
	}
	// Administrador sem o token CSRF.
	a := e.browser()
	a.login("admin@exemplo.org", pw)
	if st, _, _ := a.do("POST", "/enroll/package", form); st != http.StatusForbidden {
		t.Fatalf("sem CSRF: %d", st)
	}
	// O mesmo administrador, com CSRF, consegue.
	if st, _, _ := a.pkg(tok, "windows"); st != http.StatusOK {
		t.Fatalf("administrador: %d", st)
	}
}

func TestInstallerWithoutAgents(t *testing.T) {
	e := newEnv(t)
	e.org("Org", "admin@exemplo.org")
	b := e.browser()
	b.login("admin@exemplo.org", pw)
	tok, page := b.newToken("1")
	if strings.Contains(page, `action="/enroll/package"`) || !strings.Contains(page, "não tem os executáveis do agente") || !strings.Contains(page, "--agent-dir") {
		t.Fatal("sem executáveis, a tela deveria explicar e não oferecer o pacote")
	}
	if st, _, _ := b.pkg(tok, "windows"); st != http.StatusBadRequest {
		t.Fatalf("pacote sem executáveis: %d", st)
	}
	if st, _ := b.get("/download/SHA256SUMS"); st != http.StatusNotFound {
		t.Fatalf("SHA256SUMS sem executáveis: %d", st)
	}
	if st, _ := b.get("/download/agent/windows-amd64"); st != http.StatusNotFound {
		t.Fatalf("agente sem executáveis: %d", st)
	}
}
