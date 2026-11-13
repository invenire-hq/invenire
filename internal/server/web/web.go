// Package web é o painel do invenire-server: HTML renderizado no servidor
// com html/template, embutido no binário, sob / na mesma porta da API (que
// continua em /v1).
//
// O painel não tem regra de negócio própria: toda leitura e toda mudança
// passam pelas funções do pacote api, as mesmas das rotas /v1, com o ator da
// sessão. O tenant vem sempre da sessão, nunca da URL nem do formulário.
//
// Texto vindo das máquinas inventariadas (hostname, nomes de software,
// versões, caminhos) é dado, não HTML: o html/template o escapa sempre, e
// nada aqui usa template.HTML.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/download"
	"github.com/athosbes/invenire/internal/server/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Config são as dependências do painel.
type Config struct {
	API *api.Server
	Log *slog.Logger
	// SetupCode é o código da primeira configuração. Vazio = /setup
	// recusa tudo (a instalação já tem tenant quando o serve subiu).
	SetupCode string
	// Stale é quanto tempo sem notícia do agente o torna atrasado na visão
	// geral (o --stale do status).
	Stale time.Duration
	// Agents são os executáveis do agente que o servidor distribui (rotas
	// /download/ e pacote do instalador). Nil = nenhum; o painel explica.
	Agents *download.Agents
	// Now, para os testes.
	Now func() time.Time
}

// Panel é o painel web.
type Panel struct {
	api   *api.Server
	q     *store.Queries
	log   *slog.Logger
	stale time.Duration
	now   func() time.Time
	pages map[string]*template.Template

	setupMu   sync.Mutex
	setupCode string

	loginLimit *limiter
	setupLimit *limiter
	exports    *exportKeys
	agents     *download.Agents
}

// New monta o painel.
func New(cfg Config) (*Panel, error) {
	if cfg.API == nil {
		return nil, errors.New("web: API obrigatória")
	}
	p := &Panel{
		api: cfg.API, q: store.New(cfg.API.Pool()), log: cfg.Log, stale: cfg.Stale, now: cfg.Now,
		setupCode:  normalizeSetupCode(cfg.SetupCode),
		loginLimit: newLimiter(10, 15*time.Minute),
		setupLimit: newLimiter(10, 15*time.Minute),
		exports:    &exportKeys{m: map[string]exportKey{}},
		agents:     cfg.Agents,
	}
	if p.agents == nil {
		p.agents = download.Open("")
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	if p.stale == 0 {
		p.stale = 12 * time.Hour
	}
	if p.now == nil {
		p.now = time.Now
	}
	var err error
	if p.pages, err = parseTemplates(); err != nil {
		return nil, err
	}
	return p, nil
}

// NewSetupCode gera um código de configuração: 12 caracteres de um alfabeto
// sem letras que se confundem (cerca de 60 bits), em três grupos.
func NewSetupCode() string {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	b := make([]byte, 12)
	rand.Read(b)
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return sb.String()
}

func normalizeSetupCode(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		}
		return r
	}, strings.TrimSpace(s))
}

// Handler devolve as rotas do painel. Quem chama põe a API em /v1/ e
// /healthz e o painel no resto.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	}))

	// O agente e os scripts não são segredo: rotas públicas. O pacote com o
	// token é outra coisa (POST /enroll/package, só administrador).
	mux.HandleFunc("GET /download/agent/{platform}", p.agents.ServeAgent)
	mux.HandleFunc("GET /download/SHA256SUMS", p.agents.ServeSums)
	mux.HandleFunc("GET /download/instalar.ps1", p.installScript("ps1"))
	mux.HandleFunc("GET /download/instalar.sh", p.installScript("sh"))

	mux.HandleFunc("GET /setup", p.setupForm)
	mux.HandleFunc("POST /setup", p.setupSubmit)
	mux.HandleFunc("GET /login", p.loginForm)
	mux.HandleFunc("POST /login", p.loginSubmit)
	mux.Handle("POST /logout", p.auth(false, p.logout))

	read := func(h handler) http.Handler { return p.auth(false, h) }
	admin := func(h handler) http.Handler { return p.auth(true, h) }

	mux.Handle("GET /{$}", read(p.overview))
	mux.Handle("GET /enroll", read(p.enrollPage))
	mux.Handle("POST /enroll", admin(p.enrollCreate))
	mux.Handle("POST /enroll/package", admin(p.enrollPackage))
	mux.Handle("POST /enroll/{id}/revoke", admin(p.enrollRevoke))
	mux.Handle("GET /assets", read(p.assetsPage))
	mux.Handle("GET /assets/{id}", read(p.assetPage))
	mux.Handle("POST /assets/{id}/unit", admin(p.assetSetUnit))
	mux.Handle("POST /assets/{id}/labels", admin(p.assetSetLabel))
	mux.Handle("POST /assets/{id}/labels/remove", admin(p.assetRemoveLabel))
	mux.Handle("POST /assets/{id}/revoke", admin(p.assetRevokeAgent))
	mux.Handle("GET /units", read(p.unitsPage))
	mux.Handle("POST /units", admin(p.unitCreate))
	mux.Handle("GET /units/{id}", read(p.unitPage))
	mux.Handle("POST /units/{id}", admin(p.unitUpdate))
	mux.Handle("POST /units/{id}/delete", admin(p.unitDelete))
	mux.Handle("GET /software", read(p.softwarePage))
	mux.Handle("GET /software/unmapped", read(p.unmappedPage))
	mux.Handle("GET /software/{id}", read(p.productPage))
	mux.Handle("POST /software/raw/{id}/map", admin(p.rawMap))
	mux.Handle("POST /software/raw/{id}/unmap", admin(p.rawUnmap))
	mux.Handle("GET /access", admin(p.accessPage))
	mux.Handle("POST /access/users", admin(p.userCreate))
	mux.Handle("POST /access/actors/{id}/disable", admin(p.actorDisable))
	mux.Handle("POST /access/tokens", admin(p.apiTokenCreate))
	mux.Handle("POST /access/tokens/{id}/revoke", admin(p.apiTokenRevoke))
	mux.Handle("GET /export", admin(p.exportPage))
	mux.Handle("POST /export", admin(p.exportStart))
	mux.Handle("GET /export/download/{id}", admin(p.exportDownload))
	mux.Handle("GET /account", read(p.accountPage))
	mux.Handle("POST /account/password", read(p.accountPassword))
	mux.Handle("POST /organization", read(p.switchOrganization))

	// Formulários só aceitam envio da própria origem (Sec-Fetch-Site ou
	// Origin): é a proteção CSRF do login e do setup, que ainda não têm
	// sessão. As telas logadas conferem também o token do formulário.
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, msg("error.cross_origin"), http.StatusForbidden)
	}))
	return securityHeaders(cop.Handler(mux))
}

type handler func(w http.ResponseWriter, r *http.Request, s *session)

// securityHeaders: nada de fora da própria origem, nada de moldura, e páginas
// fora do cache (elas mostram tokens e dados do parque).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP é o endereço de quem pede. Atrás de proxy, o ProxyHeaders já pôs
// aqui o X-Forwarded-For confiável.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		if a.Is6() {
			// Um /64 é de um cliente só: limitar por endereço inteiro seria fácil de contornar.
			return netip.PrefixFrom(a, 64).Masked().String()
		}
		return a.String()
	}
	return host
}

// fail mostra o erro de uma ação: o de quem pediu, com a mensagem; o
// interno, só no log.
func (p *Panel) fail(w http.ResponseWriter, r *http.Request, s *session, err error) {
	status, text, mine := api.ErrorMessage(err)
	if !mine {
		p.log.ErrorContext(r.Context(), "erro no painel", "path", r.URL.Path, "err", err)
		text = msg("error.internal")
	}
	p.render(w, r, status, "error", s, pageData{Title: msg("error.title"), Data: text})
}

func (p *Panel) notFound(w http.ResponseWriter, r *http.Request, s *session) {
	p.render(w, r, http.StatusNotFound, "error", s, pageData{Title: msg("error.title"), Data: msg("error.not_found")})
}

// ctxWithActor é o contexto com o ator da sessão, como as rotas /v1 têm.
func ctxWithActor(r *http.Request, s *session) context.Context {
	return api.WithActor(r.Context(), s.Actor)
}
