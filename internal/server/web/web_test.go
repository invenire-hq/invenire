package web_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/download"
	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/server/web"
	"github.com/athosbes/invenire/internal/testdb"
	"github.com/athosbes/invenire/internal/wire"
	"github.com/athosbes/invenire/pkg/snapshot"
)

const setupCode = "ABCD-EFGH-JKLM"

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *store.Queries
	api  *api.Server
	srv  *httptest.Server
	ca   *pki.CA

	urlsMu sync.Mutex
	urls   []string
}

// newEnv sobe API e painel como o serve: a API em /v1/ e /healthz, o painel
// no resto, sobre TLS, com a CA da instalação (modo pin).
func newEnv(t *testing.T) *env { return newEnvWith(t, envOpts{}) }

type envOpts struct {
	// public: o servidor usa um certificado "público" (de uma CA que não é a
	// da instalação), sem pin, como com --tls-cert ou atrás de proxy.
	public bool
	agents *download.Agents
}

func newEnvWith(t *testing.T, o envOpts) *env {
	pool := testdb.New(t)
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk, err := pki.NewMasterKey(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := api.Config{ServerCA: ca, MasterKey: mk}
	if o.public {
		cfg.ServerCA = nil
	}
	s, err := api.New(pool, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	panel, err := web.New(web.Config{API: s, Log: log, SetupCode: setupCode, Agents: o.agents})
	if err != nil {
		t.Fatal(err)
	}
	root := http.NewServeMux()
	root.Handle("/v1/", s.Handler())
	root.Handle("/healthz", s.Handler())
	root.Handle("/", panel.Handler())
	cert, err := ca.ServerCert([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, q: store.New(pool), api: s, ca: ca}
	// Toda URL pedida fica registrada: o token de cadastro não pode aparecer
	// em nenhuma (URL vai para log de proxy).
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.urlsMu.Lock()
		e.urls = append(e.urls, r.URL.String())
		e.urlsMu.Unlock()
		root.ServeHTTP(w, r)
	}))
	srv.TLS = s.TLSConfig(cert)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	e.srv = srv
	return e
}

// browser é um navegador: guarda cookies e não segue redirecionamentos, para
// os testes verem cada resposta.
type browser struct {
	e    *env
	c    *http.Client
	csrf string
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{
		Jar:           jar,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: e.ca.Pool(), ServerName: "127.0.0.1"}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (b *browser) do(method, path string, form url.Values) (int, string, http.Header) {
	b.e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, b.e.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if m := csrfRe.FindSubmatch(raw); m != nil {
		b.csrf = string(m[1])
	}
	return resp.StatusCode, string(raw), resp.Header
}

func (b *browser) get(path string) (int, string) {
	st, body, _ := b.do("GET", path, nil)
	return st, body
}

// post envia o formulário com o token CSRF da última página lida.
func (b *browser) post(path string, form url.Values) (int, string, http.Header) {
	if form == nil {
		form = url.Values{}
	}
	if form.Get("csrf") == "" && b.csrf != "" {
		form.Set("csrf", b.csrf)
	}
	return b.do("POST", path, form)
}

func (b *browser) login(email, pw string) {
	b.e.t.Helper()
	st, _, h := b.do("POST", "/login", url.Values{"email": {email}, "password": {pw}})
	if st != http.StatusSeeOther || h.Get("Location") != "/" {
		b.e.t.Fatalf("login de %s: %d %s", email, st, h.Get("Location"))
	}
	if st, _ := b.get("/"); st != http.StatusOK {
		b.e.t.Fatalf("visão geral depois do login: %d", st)
	}
}

const pw = "senha-de-teste-123"

func (e *env) org(name, email string) api.Organization {
	e.t.Helper()
	o, err := api.CreateOrganization(context.Background(), e.pool, api.OrgSpec{Name: name, AdminName: "Admin " + name, AdminEmail: email, AdminPassword: pw})
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *env) asset(tenant uuid.UUID, host string) store.UpsertAssetRow {
	e.t.Helper()
	os := "windows"
	a, err := e.q.UpsertAsset(context.Background(), store.UpsertAssetParams{TenantID: tenant, Fingerprint: "machine:" + uuid.NewString(), Hostname: &host, OsFamily: &os})
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) changes(tenant uuid.UUID) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), "SELECT count(*) FROM change_log WHERE tenant_id = $1", tenant).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestSetup(t *testing.T) {
	e := newEnv(t)
	b := e.browser()

	if st, _, h := b.do("GET", "/", nil); st != http.StatusSeeOther || h.Get("Location") != "/setup" {
		t.Fatalf("instalação vazia: / deveria levar a /setup, veio %d %s", st, h.Get("Location"))
	}
	if st, _, h := b.do("GET", "/login", nil); st != http.StatusSeeOther || h.Get("Location") != "/setup" {
		t.Fatalf("instalação vazia: /login deveria levar a /setup, veio %d %s", st, h.Get("Location"))
	}
	form := url.Values{"org": {"Minha Organização"}, "name": {"Ana"}, "email": {"Ana@Exemplo.org"}, "password": {pw}, "password2": {pw}}

	// Sem o código, ou com o código errado: recusado, nada criado.
	for _, code := range []string{"", "ABCD-EFGH-JKLX"} {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set("code", code)
		if st, _, _ := b.do("POST", "/setup", f); st != http.StatusForbidden {
			t.Fatalf("setup com código %q: %d, queria 403", code, st)
		}
	}
	if n, _ := e.q.CountTenants(context.Background()); n != 0 {
		t.Fatalf("setup recusado criou %d tenant(s)", n)
	}

	// Texto em Latin-1 (curl de um terminal que não é UTF-8): 400, e não o
	// erro interno do PostgreSQL recusando a codificação.
	latin1 := url.Values{}
	for k, v := range form {
		latin1[k] = v
	}
	latin1.Set("code", "abcdefghjklm")
	latin1.Set("org", "Organiza\xe7\xe3o")
	if st, body, _ := b.do("POST", "/setup", latin1); st != http.StatusBadRequest || !strings.Contains(body, "UTF-8") {
		t.Fatalf("setup com texto que não é UTF-8: %d, queria 400 (%q)", st, body)
	}
	if n, _ := e.q.CountTenants(context.Background()); n != 0 {
		t.Fatalf("setup em Latin-1 criou %d tenant(s)", n)
	}

	// Código certo (sem hífens e em minúsculas também vale).
	form.Set("code", "abcdefghjklm")
	st, _, h := b.do("POST", "/setup", form)
	if st != http.StatusSeeOther || !strings.HasPrefix(h.Get("Location"), "/enroll") {
		t.Fatalf("setup: %d %s", st, h.Get("Location"))
	}
	st, body := b.get("/enroll?primeira=1")
	if st != http.StatusOK || !strings.Contains(body, "Minha Organização") || !strings.Contains(body, "Novo token de cadastro") {
		t.Fatalf("depois do setup, já logado em Adicionar máquinas: %d", st)
	}
	login, err := e.q.GetLoginByEmail(context.Background(), "ana@exemplo.org")
	if err != nil {
		t.Fatal("o e-mail deveria ficar em minúsculas:", err)
	}
	if !strings.HasPrefix(login.PasswordHash, "$argon2id$") {
		t.Fatalf("hash da senha: %q", login.PasswordHash)
	}

	// Com tenant: o setup não vale mais, nem com o código.
	other := e.browser()
	form.Set("email", "outra@exemplo.org")
	if st, _, _ := other.do("POST", "/setup", form); st != http.StatusConflict {
		t.Fatalf("segundo setup: %d, queria 409", st)
	}
	if st, _, h := other.do("GET", "/setup", nil); st != http.StatusSeeOther || h.Get("Location") != "/" {
		t.Fatalf("GET /setup com tenant: %d %s", st, h.Get("Location"))
	}
	if n, _ := e.q.CountTenants(context.Background()); n != 1 {
		t.Fatalf("%d tenants, queria 1", n)
	}
	// Mesmo numa instalação que ganhou tenant pela linha de comando, a
	// função recusa a primeira configuração.
	if _, err := api.CreateOrganization(context.Background(), e.pool, api.OrgSpec{Name: "X", AdminName: "X", AdminEmail: "x@exemplo.org", AdminPassword: pw, OnlyIfFirst: true}); err != api.ErrAlreadySetUp {
		t.Fatalf("OnlyIfFirst com tenant: %v", err)
	}
}

// Fluxo do piloto pelo painel: token criado na tela, máquina cadastrada com
// ele, máquina na visão geral.
func TestPanelFlow(t *testing.T) {
	e := newEnv(t)
	o := e.org("Org", "admin@exemplo.org")
	b := e.browser()
	b.login("admin@exemplo.org", pw)

	b.get("/units")
	if st, _, _ := b.post("/units", url.Values{"name": {"Laboratório"}}); st != http.StatusSeeOther {
		t.Fatalf("criar unidade: %d", st)
	}
	units, _ := e.q.ListUnits(context.Background(), o.Tenant.ID)
	if len(units) != 1 {
		t.Fatalf("%d unidades", len(units))
	}
	b.get("/enroll")
	st, body, _ := b.post("/enroll", url.Values{"unit": {units[0].ID.String()}, "uses": {"2"}, "ttl": {"24h"}, "description": {"lote 1"}})
	if st != http.StatusOK {
		t.Fatalf("criar token: %d", st)
	}
	tok := regexp.MustCompile(`inv_[A-Za-z0-9_-]{43}`).FindString(body)
	if tok == "" || !strings.Contains(body, "sha256:") || !strings.Contains(body, "instalar.ps1 -Server "+e.srv.URL) {
		t.Fatalf("a tela do token deveria mostrar token, pin e comandos:\n%s", body)
	}

	// O agente se cadastra com o token mostrado.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agente"}}, key)
	req, _ := json.Marshal(wire.EnrollRequest{
		Token: tok, CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), AgentVersion: "0.1.0",
		Host: snapshot.Host{Hostname: "<script>alert(1)</script>", SMBIOSUUID: "081B0196-A1A3-65A2-8D37-08626699AC78"},
		OS:   snapshot.OS{Family: "windows", Name: "Windows 11 Pro"},
	})
	resp, err := b.c.Post(e.srv.URL+wire.PathEnroll, "application/json", strings.NewReader(string(req)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll com o token do painel: %d", resp.StatusCode)
	}

	st, body = b.get("/")
	if st != http.StatusOK || !strings.Contains(body, "sem coleta") || !strings.Contains(body, "Laboratório") {
		t.Fatalf("a máquina deveria aparecer na visão geral, sem coleta, na unidade do token:\n%s", body)
	}
	// Texto da máquina é escapado, sempre.
	if strings.Contains(body, "<script>alert(1)</script>") || !strings.Contains(body, html.EscapeString("<script>alert(1)</script>")) {
		t.Fatal("hostname deveria sair escapado na visão geral")
	}
	assets, _ := e.api.ListAssets(context.Background(), o.Tenant.ID, url.Values{})
	id := assets.Items[0].ID
	if _, body := b.get("/assets/" + id.String()); strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("hostname deveria sair escapado no detalhe")
	}

	// Etiqueta pelo painel: vai para o change_log com o ator da sessão.
	if st, _, _ := b.post("/assets/"+id.String()+"/labels", url.Values{"key": {"ambiente"}, "value": {"<b>produção</b>"}}); st != http.StatusSeeOther {
		t.Fatalf("etiqueta: %d", st)
	}
	var actor *uuid.UUID
	var kind string
	if err := e.pool.QueryRow(context.Background(), "SELECT kind, actor_id FROM change_log WHERE tenant_id = $1 ORDER BY id DESC LIMIT 1", o.Tenant.ID).Scan(&kind, &actor); err != nil {
		t.Fatal(err)
	}
	if kind != "label_set" || actor == nil || *actor != o.Admin.ID {
		t.Fatalf("change_log: %s por %v, queria label_set por %s", kind, actor, o.Admin.ID)
	}
	if _, body := b.get("/assets/" + id.String()); strings.Contains(body, "<b>produção</b>") {
		t.Fatal("valor da etiqueta deveria sair escapado")
	}

	// Revogar o agente e o token pelo painel.
	if st, _, _ := b.post("/assets/"+id.String()+"/revoke", nil); st != http.StatusSeeOther {
		t.Fatalf("revogar agente: %d", st)
	}
	if _, body := b.get("/"); !strings.Contains(body, "sem agente") {
		t.Fatal("depois de revogar, a máquina deveria estar sem agente")
	}
	toks, _ := e.q.ListEnrollmentTokens(context.Background(), store.ListEnrollmentTokensParams{TenantID: o.Tenant.ID})
	b.get("/enroll")
	if st, _, _ := b.post("/enroll/"+toks[0].ID.String()+"/revoke", nil); st != http.StatusSeeOther {
		t.Fatalf("revogar token: %d", st)
	}
	if toks, _ := e.q.ListEnrollmentTokens(context.Background(), store.ListEnrollmentTokensParams{TenantID: o.Tenant.ID}); len(toks) != 0 {
		t.Fatal("token revogado ainda válido")
	}

	// Sair encerra a sessão: o cookie antigo não vale mais.
	b.get("/")
	if st, _, h := b.post("/logout", nil); st != http.StatusSeeOther || h.Get("Location") != "/login" {
		t.Fatalf("sair: %d", st)
	}
	if st, _, h := b.do("GET", "/", nil); st != http.StatusSeeOther || !strings.HasPrefix(h.Get("Location"), "/login") {
		t.Fatalf("depois de sair: %d %s", st, h.Get("Location"))
	}
}

// Um usuário read vê, mas não muda nada; e nenhum formulário passa sem o
// token CSRF da sessão.
func TestReadUserCannotChange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	o := e.org("Org", "admin@exemplo.org")
	if _, err := e.api.CreatePanelUser(ctx, o.Admin, api.NewPanelUser{Name: "Leitor", Email: "leitor@exemplo.org", Password: pw}); err != nil {
		t.Fatal(err)
	}
	a := e.asset(o.Tenant.ID, "pc-01")
	unit, err := e.api.AddUnit(ctx, o.Admin, api.UnitCreate{Name: "Setor"})
	if err != nil {
		t.Fatal(err)
	}
	lbl := "x"
	if _, err := e.api.ChangeLabel(ctx, o.Admin, a.ID, "k", &lbl); err != nil {
		t.Fatal(err)
	}
	var tokID uuid.UUID
	e.api.InTx(ctx, func(q *store.Queries) error {
		tok, err := api.IssueEnrollmentToken(ctx, q, &o.Admin.ID, api.TokenSpec{Tenant: o.Tenant.ID, TTL: time.Hour, Uses: 1})
		tokID = tok.ID
		return err
	})
	before := e.changes(o.Tenant.ID)

	b := e.browser()
	b.login("leitor@exemplo.org", pw)
	for _, path := range []string{"/", "/enroll", "/assets", "/assets/" + a.ID.String(), "/units", "/units/" + unit.Unit.ID.String(), "/software", "/software/unmapped", "/account"} {
		st, body := b.get(path)
		if st != http.StatusOK {
			t.Fatalf("leitor em %s: %d", path, st)
		}
		if strings.Contains(body, `action="/enroll"`) || strings.Contains(body, "/revoke") || strings.Contains(body, `action="/units"`) {
			t.Fatalf("a página %s não deveria mostrar formulários de alteração ao leitor", path)
		}
	}
	for _, path := range []string{"/access", "/export"} {
		if st, _ := b.get(path); st != http.StatusForbidden {
			t.Fatalf("leitor em %s: %d, queria 403", path, st)
		}
	}
	b.get("/")
	id := a.ID.String()
	posts := map[string]url.Values{
		"/enroll":                                            {"uses": {"1"}, "ttl": {"24h"}},
		"/enroll/" + tokID.String() + "/revoke":              nil,
		"/assets/" + id + "/unit":                            {"unit": {unit.Unit.ID.String()}},
		"/assets/" + id + "/labels":                          {"key": {"novo"}, "value": {"y"}},
		"/assets/" + id + "/labels/remove":                   {"key": {"k"}},
		"/assets/" + id + "/revoke":                          nil,
		"/units":                                             {"name": {"Outra"}},
		"/units/" + unit.Unit.ID.String():                    {"name": {"Renomeada"}},
		"/units/" + unit.Unit.ID.String() + "/delete":        nil,
		"/software/raw/" + uuid.NewString() + "/map":         {"product": {"X"}},
		"/access/users":                                      {"name": {"Z"}, "email": {"z@exemplo.org"}, "password": {pw}, "password2": {pw}, "scope": {"admin"}},
		"/access/actors/" + o.Admin.ID.String() + "/disable": nil,
		"/access/tokens":                                     {"name": {"ci"}, "kind": {"ci"}, "scope": {"admin"}, "ttl": {"0"}},
		"/export":                                            nil,
	}
	for path, form := range posts {
		if st, _, _ := b.post(path, form); st != http.StatusForbidden {
			t.Errorf("leitor POST %s: %d, queria 403", path, st)
		}
	}
	if after := e.changes(o.Tenant.ID); after != before {
		t.Fatalf("o leitor mudou o change_log: %d → %d", before, after)
	}

	// O administrador, sem o token CSRF, também não muda nada.
	adm := e.browser()
	adm.login("admin@exemplo.org", pw)
	if st, _, _ := adm.do("POST", "/units", url.Values{"name": {"Sem CSRF"}}); st != http.StatusForbidden {
		t.Fatalf("POST sem CSRF: %d, queria 403", st)
	}
	if st, _, _ := adm.do("POST", "/units", url.Values{"name": {"CSRF de outro"}, "csrf": {"x" + b.csrf[1:]}}); st != http.StatusForbidden {
		t.Fatalf("POST com CSRF errado: %d, queria 403", st)
	}
	// Formulário enviado por outro site (o navegador manda Sec-Fetch-Site).
	req, _ := http.NewRequest("POST", e.srv.URL+"/units", strings.NewReader(url.Values{"name": {"X"}, "csrf": {adm.csrf}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := adm.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST de outro site: %d, queria 403", resp.StatusCode)
	}
	if after := e.changes(o.Tenant.ID); after != before {
		t.Fatalf("POST recusado mudou o change_log: %d → %d", before, after)
	}
	// Com o token, passa.
	adm.get("/units")
	if st, _, _ := adm.post("/units", url.Values{"name": {"Com CSRF"}}); st != http.StatusSeeOther {
		t.Fatalf("POST do administrador: %d", st)
	}
}

// Quem é de um tenant não vê nem mexe em nada do outro.
func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	oa := e.org("Org A", "a@exemplo.org")
	ob := e.org("Org B", "b@exemplo.org")

	assetA := e.asset(oa.Tenant.ID, "maquina-secreta-a")
	if _, err := e.q.CreateAgent(ctx, store.CreateAgentParams{ID: uuid.New(), AssetID: assetA.ID, CertFingerprint: "fp-a", AgentVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	e.asset(ob.Tenant.ID, "maquina-de-b")
	unitA, err := e.api.AddUnit(ctx, oa.Admin, api.UnitCreate{Name: "Unidade-Secreta-A"})
	if err != nil {
		t.Fatal(err)
	}
	var tokA uuid.UUID
	e.api.InTx(ctx, func(q *store.Queries) error {
		tok, err := api.IssueEnrollmentToken(ctx, q, &oa.Admin.ID, api.TokenSpec{Tenant: oa.Tenant.ID, Description: "token-secreto-a", TTL: time.Hour, Uses: 1})
		tokA = tok.ID
		return err
	})
	if _, err := e.api.CreateAPIClient(ctx, oa.Admin, api.NewAPIClient{Name: "pipeline-secreto-a", Kind: "ci"}); err != nil {
		t.Fatal(err)
	}
	beforeA := e.changes(oa.Tenant.ID)

	b := e.browser()
	b.login("b@exemplo.org", pw) // administrador de B
	for _, path := range []string{"/", "/enroll?todos=1", "/assets", "/units", "/software", "/software/unmapped", "/access"} {
		st, body := b.get(path)
		if st != http.StatusOK {
			t.Fatalf("B em %s: %d", path, st)
		}
		for _, secret := range []string{"maquina-secreta-a", "Unidade-Secreta-A", "token-secreto-a", "pipeline-secreto-a", "a@exemplo.org", "Org A"} {
			if strings.Contains(body, secret) {
				t.Fatalf("B vê %q de A em %s", secret, path)
			}
		}
	}
	if _, body := b.get("/"); !strings.Contains(body, "maquina-de-b") {
		t.Fatal("B deveria ver a própria máquina")
	}
	for _, path := range []string{"/assets/" + assetA.ID.String(), "/units/" + unitA.Unit.ID.String(), "/assets?unit=" + unitA.Unit.ID.String()} {
		if st, _ := b.get(path); st != http.StatusNotFound {
			t.Errorf("B em %s de A: %d, queria 404", path, st)
		}
	}
	b.get("/")
	idA := assetA.ID.String()
	for path, form := range map[string]url.Values{
		"/assets/" + idA + "/labels":                   {"key": {"invasao"}, "value": {"x"}},
		"/assets/" + idA + "/unit":                     {"unit": {""}},
		"/assets/" + idA + "/revoke":                   nil,
		"/units/" + unitA.Unit.ID.String():             {"name": {"Tomada"}},
		"/units/" + unitA.Unit.ID.String() + "/delete": nil,
		"/units":                               {"name": {"Filha"}, "parent": {unitA.Unit.ID.String()}},
		"/enroll":                              {"unit": {unitA.Unit.ID.String()}, "uses": {"1"}, "ttl": {"1h"}},
		"/enroll/" + tokA.String() + "/revoke": nil,
		"/access/actors/" + oa.Admin.ID.String() + "/disable": nil,
	} {
		st, _, h := b.post(path, form)
		if st == http.StatusSeeOther {
			// Erro de quem pediu volta com aviso; conferimos o aviso abaixo
			// e que A não mudou.
			_, body := b.get(h.Get("Location"))
			if !strings.Contains(body, "flash error") {
				t.Errorf("B POST %s em coisa de A: passou (%s)", path, h.Get("Location"))
			}
			continue
		}
		if st != http.StatusNotFound {
			t.Errorf("B POST %s em coisa de A: %d", path, st)
		}
	}
	if after := e.changes(oa.Tenant.ID); after != beforeA {
		t.Fatalf("B mudou o change_log de A: %d → %d", beforeA, after)
	}
	if toks, _ := e.q.ListEnrollmentTokens(ctx, store.ListEnrollmentTokensParams{TenantID: oa.Tenant.ID}); len(toks) != 1 {
		t.Fatal("o token de A deveria continuar válido")
	}
	if adm, _ := e.q.GetActorInTenant(ctx, store.GetActorInTenantParams{ID: oa.Admin.ID, TenantID: oa.Tenant.ID}); adm.DisabledAt != nil {
		t.Fatal("B desativou o administrador de A")
	}
}

func TestLoginLimitAndRecovery(t *testing.T) {
	e := newEnv(t)
	e.org("Org", "admin@exemplo.org")

	// A recuperação pela linha de comando troca a senha e derruba as sessões.
	c := e.browser()
	c.login("admin@exemplo.org", pw)
	if err := api.SetPassword(context.Background(), e.pool, "ADMIN@exemplo.org", "outra-senha-boa-1", nil); err != nil {
		t.Fatal(err)
	}
	if st, _, h := c.do("GET", "/", nil); st != http.StatusSeeOther || !strings.HasPrefix(h.Get("Location"), "/login") {
		t.Fatalf("sessão aberta antes da troca de senha: %d %s", st, h.Get("Location"))
	}
	if st, _, _ := c.do("POST", "/login", url.Values{"email": {"admin@exemplo.org"}, "password": {pw}}); st != http.StatusUnauthorized {
		t.Fatalf("senha antiga: %d, queria 401", st)
	}
	c.login("admin@exemplo.org", "outra-senha-boa-1")

	b := e.browser()
	for i := 0; i < 9; i++ { // mais a falha de c acima: 10
		if st, _, _ := b.do("POST", "/login", url.Values{"email": {"admin@exemplo.org"}, "password": {"errada-errada"}}); st != http.StatusUnauthorized {
			t.Fatalf("tentativa %d: %d", i, st)
		}
	}
	// Agora nem a senha certa passa, até a janela andar.
	if st, _, _ := b.do("POST", "/login", url.Values{"email": {"admin@exemplo.org"}, "password": {"outra-senha-boa-1"}}); st != http.StatusTooManyRequests {
		t.Fatalf("depois de 10 falhas: %d, queria 429", st)
	}
}
