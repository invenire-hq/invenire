package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/password"
	"github.com/athosbes/invenire/internal/server/store"
)

const (
	// __Host-: o navegador só aceita o cookie com Secure, Path=/ e sem
	// Domain, então outro site do mesmo domínio não o sobrescreve.
	sessionCookie = "__Host-invenire-sessao"
	flashCookie   = "__Host-invenire-aviso"

	// Sessão cai depois de 12h sem uso, e de 7 dias em qualquer caso.
	sessionIdle     = 12 * time.Hour
	sessionAbsolute = 7 * 24 * time.Hour
	touchEvery      = time.Minute
)

// session é a pessoa logada, no tenant do ator da sessão.
type session struct {
	Actor      store.Actor
	LoginID    uuid.UUID
	Email      string
	TenantName string
	Admin      bool
	CSRF       string
	// Orgs são as outras organizações do mesmo login (o serviço hospedado
	// futuro); vazio no caso comum.
	Orgs   []store.ListLoginActorsRow
	idHash []byte
}

func hashID(id string) []byte {
	h := sha256.Sum256([]byte(id))
	return h[:]
}

// csrfToken deriva o token dos formulários do identificador da sessão: quem
// não lê o cookie (HttpOnly) não sabe o token, e nada fica guardado.
func csrfToken(sessionID string) string {
	h := sha256.Sum256([]byte("invenire-csrf\x00" + sessionID))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func newSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// startSession cria a sessão do ator e põe o cookie. Uma sessão anterior no
// mesmo navegador é apagada: o identificador muda a cada login.
func (p *Panel) startSession(w http.ResponseWriter, r *http.Request, loginID, actorID uuid.UUID) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		p.q.DeleteSession(r.Context(), hashID(c.Value))
	}
	// Aproveita o login para tirar do banco as sessões vencidas.
	if _, err := p.q.DeleteExpiredSessions(r.Context()); err != nil {
		return err
	}
	id := newSessionID()
	if err := p.q.CreateSession(r.Context(), store.CreateSessionParams{
		IDHash: hashID(id), LoginID: loginID, ActorID: actorID, ExpiresAt: p.now().Add(sessionIdle),
	}); err != nil {
		return err
	}
	setCookie(w, sessionCookie, id, int(sessionAbsolute/time.Second))
	return nil
}

// loadSession lê a sessão do cookie; nil se não há sessão válida.
func (p *Panel) loadSession(r *http.Request) (*session, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	row, err := p.q.GetSession(r.Context(), hashID(c.Value))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := p.now()
	if now.Sub(row.SessionCreatedAt) > sessionAbsolute {
		p.q.DeleteSession(r.Context(), row.IDHash)
		return nil, nil
	}
	if now.Sub(row.LastSeenAt) > touchEvery {
		exp := now.Add(sessionIdle)
		if limit := row.SessionCreatedAt.Add(sessionAbsolute); exp.After(limit) {
			exp = limit
		}
		if err := p.q.TouchSession(r.Context(), store.TouchSessionParams{IDHash: row.IDHash, ExpiresAt: exp}); err != nil {
			return nil, err
		}
	}
	s := &session{
		Actor: row.Actor, LoginID: row.LoginID, Email: row.Email, TenantName: row.TenantName,
		Admin: api.HasScope(row.Actor, api.ScopeAdmin), CSRF: csrfToken(c.Value), idHash: row.IDHash,
	}
	orgs, err := p.q.ListLoginActors(r.Context(), &row.LoginID)
	if err != nil {
		return nil, err
	}
	if len(orgs) > 1 {
		s.Orgs = orgs
	}
	return s, nil
}

// auth exige sessão com o escopo read (ou admin, se needAdmin). Envio de
// formulário exige também o token CSRF da sessão.
func (p *Panel) auth(needAdmin bool, next handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n, err := p.q.CountTenants(r.Context()); err == nil && n == 0 {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		s, err := p.loadSession(r)
		if err != nil {
			p.log.ErrorContext(r.Context(), "sessão do painel", "err", err)
			http.Error(w, msg("error.internal"), http.StatusInternalServerError)
			return
		}
		if s == nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login?volta="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(s.CSRF)) != 1 {
				p.render(w, r, http.StatusForbidden, "error", s, pageData{Title: msg("error.title"), Data: msg("error.csrf")})
				return
			}
		}
		if !api.HasScope(s.Actor, api.ScopeRead) || (needAdmin && !s.Admin) {
			p.render(w, r, http.StatusForbidden, "error", s, pageData{Title: msg("error.title"), Data: msg("error.forbidden")})
			return
		}
		next(w, r, s)
	})
}

// safeReturn só aceita voltar para um caminho do próprio painel.
func safeReturn(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") ||
		strings.HasPrefix(v, "/v1/") || strings.ContainsAny(v, "\r\n") {
		return "/"
	}
	return v
}

func (p *Panel) loginForm(w http.ResponseWriter, r *http.Request) {
	if n, err := p.q.CountTenants(r.Context()); err == nil && n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if s, _ := p.loadSession(r); s != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p.render(w, r, http.StatusOK, "login", nil, pageData{Title: msg("login.title"), Data: loginView{Return: safeReturn(r.URL.Query().Get("volta"))}})
}

type loginView struct {
	Email  string
	Return string
	Error  string
}

// loginSubmit confere e-mail e senha. As falhas contam por IP e por e-mail
// (10 a cada 15 minutos): adivinhar senha pelo painel fica lento demais, e
// um e-mail atacado de muitos lugares também para.
func (p *Panel) loginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	pw := r.PostFormValue("password")
	view := loginView{Email: email, Return: safeReturn(r.PostFormValue("volta"))}
	ipKey, emailKey := "ip:"+clientIP(r), "email:"+email
	if !p.loginLimit.allow(ipKey) || !p.loginLimit.allow(emailKey) {
		view.Error = msg("login.too_many")
		p.render(w, r, http.StatusTooManyRequests, "login", nil, pageData{Title: msg("login.title"), Data: view})
		return
	}
	fail := func() {
		p.loginLimit.fail(ipKey)
		p.loginLimit.fail(emailKey)
		p.log.WarnContext(r.Context(), "login recusado", "ip", clientIP(r))
		view.Error = msg("login.invalid")
		p.render(w, r, http.StatusUnauthorized, "login", nil, pageData{Title: msg("login.title"), Data: view})
	}
	login, err := p.q.GetLoginByEmail(r.Context(), email)
	if errors.Is(err, pgx.ErrNoRows) {
		password.Waste(pw)
		fail()
		return
	}
	if err != nil {
		p.fail(w, r, nil, err)
		return
	}
	ok, err := password.Verify(login.PasswordHash, pw)
	if err != nil {
		p.log.ErrorContext(r.Context(), "hash de senha ilegível", "login_id", login.ID, "err", err)
	}
	if !ok {
		fail()
		return
	}
	actors, err := p.q.ListLoginActors(r.Context(), &login.ID)
	if err != nil {
		p.fail(w, r, nil, err)
		return
	}
	if len(actors) == 0 {
		// A senha confere, mas o acesso foi desativado em todas as organizações.
		fail()
		return
	}
	p.loginLimit.reset(emailKey)
	if err := p.startSession(w, r, login.ID, actors[0].ID); err != nil {
		p.fail(w, r, nil, err)
		return
	}
	p.log.InfoContext(r.Context(), "login no painel", "actor_id", actors[0].ID, "tenant_id", actors[0].TenantID, "ip", clientIP(r))
	http.Redirect(w, r, view.Return, http.StatusSeeOther)
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request, s *session) {
	if err := p.q.DeleteSession(r.Context(), s.idHash); err != nil {
		p.fail(w, r, s, err)
		return
	}
	setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// switchOrganization troca a sessão para o ator do mesmo login em outra
// organização. A sessão é refeita, com identificador novo.
func (p *Panel) switchOrganization(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := uuid.Parse(r.PostFormValue("actor_id"))
	if err != nil {
		p.notFound(w, r, s)
		return
	}
	actors, err := p.q.ListLoginActors(r.Context(), &s.LoginID)
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	for _, a := range actors {
		if a.ID == id {
			if err := p.startSession(w, r, s.LoginID, a.ID); err != nil {
				p.fail(w, r, s, err)
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	p.notFound(w, r, s)
}

// flash é o aviso que sobrevive a um redirecionamento.
type flash struct {
	Error bool   `json:"e,omitempty"`
	Text  string `json:"t"`
}

func setFlash(w http.ResponseWriter, f flash) {
	b, _ := json.Marshal(f)
	setCookie(w, flashCookie, base64.RawURLEncoding.EncodeToString(b), 60)
}

func takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	setCookie(w, flashCookie, "", -1)
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	var f flash
	if err != nil || json.Unmarshal(b, &f) != nil || f.Text == "" || len(f.Text) > 1000 {
		return nil
	}
	return &f
}

// done volta para a página com um aviso; failed, com o erro da ação.
func (p *Panel) done(w http.ResponseWriter, r *http.Request, to, text string) {
	setFlash(w, flash{Text: text})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (p *Panel) failed(w http.ResponseWriter, r *http.Request, s *session, to string, err error) {
	status, text, mine := api.ErrorMessage(err)
	if !mine || status >= 500 {
		p.fail(w, r, s, err)
		return
	}
	setFlash(w, flash{Error: true, Text: text})
	http.Redirect(w, r, to, http.StatusSeeOther)
}
