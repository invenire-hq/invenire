// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/invenire-hq/invenire/internal/server/password"
	"github.com/invenire-hq/invenire/internal/server/store"
)

// Ações de administração que não têm rota /v1, mas que o painel e a linha de
// comando fazem pelas mesmas funções: organização e primeiro administrador,
// usuários do painel, tokens de API e de cadastro, revogação de agente. Toda
// mudança vai para o change_log com o ator (nulo = linha de comando).

// WithActor põe o ator no contexto, como userAuth faz para as rotas /v1.
func WithActor(ctx context.Context, a store.Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ErrorMessage diz o status e a mensagem para quem pediu. Um erro que não é
// de quem pediu (banco fora do ar, por exemplo) vira 500 sem detalhe.
func ErrorMessage(err error) (int, string, bool) {
	var he *httpError
	if errors.As(err, &he) {
		return he.status, he.msg, true
	}
	return http.StatusInternalServerError, "erro interno", false
}

// HasScope diz se o ator tem o escopo (admin vale por todos), com a mesma
// regra das rotas /v1.
func HasScope(a store.Actor, scope string) bool {
	return slices.Contains(a.Scopes, scope) || slices.Contains(a.Scopes, scopeAdmin)
}

// Escopos exportados para o painel.
const (
	ScopeRead  = scopeRead
	ScopeAdmin = scopeAdmin
)

// CAPin é o pin da CA da instalação, ou vazio quando o servidor usa um
// certificado público (direto ou no proxy).
func (s *Server) CAPin() string {
	if s.serverCA == nil {
		return ""
	}
	h := sha256.Sum256(s.serverCA.Cert.Raw)
	return "sha256:" + hex.EncodeToString(h[:])
}

// BehindProxy diz se o servidor fala HTTP atrás de um proxy que termina o TLS.
func (s *Server) BehindProxy() bool { return s.behindProxy }

// Pool é o banco do servidor, para o painel guardar as sessões.
func (s *Server) Pool() *pgxpool.Pool { return s.pool }

// Log é o log do servidor.
func (s *Server) Log() interface {
	InfoContext(context.Context, string, ...any)
	WarnContext(context.Context, string, ...any)
	ErrorContext(context.Context, string, ...any)
} {
	return s.log
}

// InTx roda fn numa transação.
func (s *Server) InTx(ctx context.Context, fn func(q *store.Queries) error) error {
	return s.inTx(ctx, fn)
}

// NormalizeEmail é o e-mail como o login o guarda: sem espaços nas pontas e
// em minúsculas.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || len(email) > 254 || !strings.Contains(email, "@") {
		return "", errorf(http.StatusBadRequest, "e-mail inválido")
	}
	return email, nil
}

func validName(what, name string, max int) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errorf(http.StatusBadRequest, "%s é obrigatório", what)
	case utf8.RuneCountInString(name) > max:
		return "", errorf(http.StatusBadRequest, "%s com mais de %d caracteres", what, max)
	case strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", errorf(http.StatusBadRequest, "%s com caracteres de controle", what)
	}
	return name, nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func logChange(ctx context.Context, q *store.Queries, tenant uuid.UUID, kind string, actor *uuid.UUID, asset *uuid.UUID, data map[string]any) (int64, error) {
	b, _ := json.Marshal(data)
	return q.AppendChange(ctx, store.AppendChangeParams{TenantID: tenant, Kind: kind, ActorID: actor, AssetID: asset, Data: b})
}

// OrgSpec descreve uma organização nova com o primeiro administrador.
type OrgSpec struct {
	Name          string
	AdminName     string
	AdminEmail    string
	AdminPassword string
	// OnlyIfFirst recusa se já existe algum tenant: é a primeira
	// configuração (/setup), que só vale numa instalação vazia.
	OnlyIfFirst bool
	// LinkExistingLogin: se o e-mail já tem login nesta instalação, liga o
	// administrador novo a ele (a senha dada é ignorada). Só para o operador
	// da instalação (linha de comando); quem não provou ser o dono do e-mail
	// não pode ganhar acesso por ele.
	LinkExistingLogin bool
}

// Organization é o que CreateOrganization criou.
type Organization struct {
	Tenant store.Tenant
	Admin  store.Actor
	// LinkedLogin diz que o e-mail já tinha login e o administrador foi
	// ligado a ele, com a senha que já tinha.
	LinkedLogin bool
}

// ErrAlreadySetUp é a recusa do /setup numa instalação que já tem tenant.
var ErrAlreadySetUp = &httpError{status: http.StatusConflict, msg: "esta instalação já foi configurada"}

// CreateOrganization cria o tenant e o primeiro administrador (ator humano
// com read e admin, e login com e-mail e senha), numa transação só. É a
// função do /setup e do tenant create --admin-email, e a que um autocadastro
// futuro deve chamar.
func CreateOrganization(ctx context.Context, pool *pgxpool.Pool, spec OrgSpec) (Organization, error) {
	var org Organization
	name, err := validName("nome da organização", spec.Name, 120)
	if err != nil {
		return org, err
	}
	adminName, err := validName("nome do administrador", spec.AdminName, 120)
	if err != nil {
		return org, err
	}
	email, err := NormalizeEmail(spec.AdminEmail)
	if err != nil {
		return org, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return org, err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)
	if spec.OnlyIfFirst {
		if err := q.LockSetup(ctx); err != nil {
			return org, err
		}
		if n, err := q.CountTenants(ctx); err != nil {
			return org, err
		} else if n > 0 {
			return org, ErrAlreadySetUp
		}
	}
	login, err := q.GetLoginByEmail(ctx, email)
	switch {
	case err == nil && !spec.LinkExistingLogin:
		return org, errorf(http.StatusConflict, "o e-mail %s já tem login nesta instalação", email)
	case err == nil:
		org.LinkedLogin = true
	case errors.Is(err, pgx.ErrNoRows):
		hash, err := password.Hash(spec.AdminPassword)
		if err != nil {
			return org, errorf(http.StatusBadRequest, "%s", err)
		}
		if login, err = q.CreateLogin(ctx, store.CreateLoginParams{Email: email, PasswordHash: hash}); err != nil {
			return org, err
		}
	default:
		return org, err
	}
	if org.Tenant, err = q.CreateTenant(ctx, name); isUnique(err) {
		return org, errorf(http.StatusConflict, "já existe uma organização chamada %q", name)
	} else if err != nil {
		return org, err
	}
	if org.Admin, err = q.CreateActor(ctx, store.CreateActorParams{
		TenantID: org.Tenant.ID, Kind: "human", Name: adminName, Scopes: []string{scopeRead, scopeAdmin},
	}); err != nil {
		return org, err
	}
	if err := q.SetActorLogin(ctx, store.SetActorLoginParams{ID: org.Admin.ID, LoginID: &login.ID}); err != nil {
		return org, err
	}
	org.Admin.LoginID = &login.ID
	if _, err := logChange(ctx, q, org.Tenant.ID, "tenant_created", &org.Admin.ID, nil, map[string]any{"name": name}); err != nil {
		return org, err
	}
	if _, err := logChange(ctx, q, org.Tenant.ID, "actor_created", &org.Admin.ID, nil, map[string]any{
		"actor_id": org.Admin.ID, "kind": "human", "name": adminName, "email": email, "scopes": org.Admin.Scopes, "panel_login": true,
	}); err != nil {
		return org, err
	}
	return org, tx.Commit(ctx)
}

// NewPanelUser é uma pessoa a cadastrar no painel.
type NewPanelUser struct {
	Name     string
	Email    string
	Password string
	Admin    bool // senão, só leitura
}

// CreatePanelUser cria um ator humano com login no tenant de quem pede.
// Um e-mail que já tem login na instalação é recusado: ganhar acesso por um
// e-mail exige provar que se é o dono dele, e isso não existe ainda.
func (s *Server) CreatePanelUser(ctx context.Context, by store.Actor, u NewPanelUser) (store.Actor, error) {
	var a store.Actor
	name, err := validName("nome", u.Name, 120)
	if err != nil {
		return a, err
	}
	email, err := NormalizeEmail(u.Email)
	if err != nil {
		return a, err
	}
	if err := password.Validate(u.Password); err != nil {
		return a, errorf(http.StatusBadRequest, "%s", err)
	}
	hash, err := password.Hash(u.Password)
	if err != nil {
		return a, err
	}
	scopes := []string{scopeRead}
	if u.Admin {
		scopes = append(scopes, scopeAdmin)
	}
	err = s.inTx(ctx, func(q *store.Queries) error {
		if _, err := q.GetLoginByEmail(ctx, email); err == nil {
			return errorf(http.StatusConflict, "o e-mail %s já tem login nesta instalação", email)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		login, err := q.CreateLogin(ctx, store.CreateLoginParams{Email: email, PasswordHash: hash})
		if isUnique(err) {
			return errorf(http.StatusConflict, "o e-mail %s já tem login nesta instalação", email)
		} else if err != nil {
			return err
		}
		a, err = q.CreateActor(ctx, store.CreateActorParams{TenantID: by.TenantID, Kind: "human", Name: name, Scopes: scopes})
		if isUnique(err) {
			return errorf(http.StatusConflict, "já existe uma pessoa chamada %q nesta organização", name)
		} else if err != nil {
			return err
		}
		if err := q.SetActorLogin(ctx, store.SetActorLoginParams{ID: a.ID, LoginID: &login.ID}); err != nil {
			return err
		}
		a.LoginID = &login.ID
		_, err = logChange(ctx, q, by.TenantID, "actor_created", &by.ID, nil, map[string]any{
			"actor_id": a.ID, "kind": "human", "name": name, "email": email, "scopes": scopes, "panel_login": true,
		})
		return err
	})
	return a, err
}

// DisableActor desativa um ator do tenant (pessoa ou pipeline): os tokens de
// API e as sessões do painel dele deixam de valer. Ninguém se desativa, e o
// último administrador do painel fica.
func (s *Server) DisableActor(ctx context.Context, by store.Actor, id uuid.UUID) error {
	if id == by.ID {
		return errorf(http.StatusBadRequest, "você não pode desativar o seu próprio acesso")
	}
	return s.inTx(ctx, func(q *store.Queries) error {
		target, err := q.GetActorInTenant(ctx, store.GetActorInTenantParams{ID: id, TenantID: by.TenantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "usuário não encontrado")
		} else if err != nil {
			return err
		}
		if target.Kind == "system" || target.Kind == "collector" {
			return errorf(http.StatusBadRequest, "este ator é do próprio servidor")
		}
		if target.DisabledAt != nil {
			return errorf(http.StatusConflict, "já desativado")
		}
		if target.LoginID != nil && slices.Contains(target.Scopes, scopeAdmin) {
			n, err := q.CountPanelAdmins(ctx, by.TenantID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return errorf(http.StatusConflict, "é o último administrador do painel")
			}
		}
		if _, err := q.DisableActorInTenant(ctx, store.DisableActorInTenantParams{ID: id, TenantID: by.TenantID}); err != nil {
			return err
		}
		if _, err := q.DeleteActorSessions(ctx, id); err != nil {
			return err
		}
		_, err = logChange(ctx, q, by.TenantID, "actor_disabled", &by.ID, nil, map[string]any{"actor_id": id, "name": target.Name, "kind": target.Kind})
		return err
	})
}

// NewAPIClient é um usuário ou pipeline da API, criado com um token.
type NewAPIClient struct {
	Name        string
	Kind        string // human, ai_agent ou ci
	Admin       bool   // senão, só leitura
	Description string
	TTL         time.Duration // 0 = não expira
}

// CreatedAPIToken é o token de API recém-criado; Token só existe aqui.
type CreatedAPIToken struct {
	Actor     store.Actor
	Token     string
	ExpiresAt *time.Time
}

// CreateAPIClient cria o ator e o primeiro token de API dele.
func (s *Server) CreateAPIClient(ctx context.Context, by store.Actor, c NewAPIClient) (CreatedAPIToken, error) {
	var out CreatedAPIToken
	name, err := validName("nome", c.Name, 120)
	if err != nil {
		return out, err
	}
	switch c.Kind {
	case "human", "ai_agent", "ci":
	default:
		return out, errorf(http.StatusBadRequest, "tipo inválido: human, ai_agent ou ci")
	}
	if utf8.RuneCountInString(c.Description) > 200 {
		return out, errorf(http.StatusBadRequest, "descrição com mais de 200 caracteres")
	}
	if c.TTL < 0 {
		return out, errorf(http.StatusBadRequest, "validade inválida")
	}
	scopes := []string{scopeRead}
	if c.Admin {
		scopes = append(scopes, scopeAdmin)
	}
	err = s.inTx(ctx, func(q *store.Queries) error {
		a, err := q.CreateActor(ctx, store.CreateActorParams{TenantID: by.TenantID, Kind: c.Kind, Name: name, Scopes: scopes})
		if isUnique(err) {
			return errorf(http.StatusConflict, "já existe %q com esse tipo nesta organização", name)
		} else if err != nil {
			return err
		}
		out.Actor = a
		if out.Token, out.ExpiresAt, err = NewAPIToken(ctx, q, a.ID, c.Description, c.TTL); err != nil {
			return err
		}
		if _, err := logChange(ctx, q, by.TenantID, "actor_created", &by.ID, nil, map[string]any{
			"actor_id": a.ID, "kind": c.Kind, "name": name, "scopes": scopes,
		}); err != nil {
			return err
		}
		_, err = logChange(ctx, q, by.TenantID, "api_token_created", &by.ID, nil, map[string]any{
			"actor_id": a.ID, "description": c.Description, "expires_at": out.ExpiresAt,
		})
		return err
	})
	return out, err
}

// RevokeAPIToken revoga um token de API do tenant.
func (s *Server) RevokeAPIToken(ctx context.Context, by store.Actor, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		row, err := q.RevokeAPITokenInTenant(ctx, store.RevokeAPITokenInTenantParams{ID: id, TenantID: by.TenantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "token não encontrado, ou já revogado")
		} else if err != nil {
			return err
		}
		_, err = logChange(ctx, q, by.TenantID, "api_token_revoked", &by.ID, nil, map[string]any{"api_token_id": row.ID, "actor_id": row.ActorID})
		return err
	})
}

// IssueEnrollmentToken cria um token de cadastro e registra
// enrollment_token_created. actor nulo = linha de comando.
func IssueEnrollmentToken(ctx context.Context, q *store.Queries, actor *uuid.UUID, spec TokenSpec) (NewToken, error) {
	if utf8.RuneCountInString(spec.Description) > 200 {
		return NewToken{}, errorf(http.StatusBadRequest, "descrição com mais de 200 caracteres")
	}
	if spec.TTL <= 0 || spec.TTL > 366*24*time.Hour {
		return NewToken{}, errorf(http.StatusBadRequest, "validade de até um ano")
	}
	if spec.Uses < 1 || spec.Uses > 10000 {
		return NewToken{}, errorf(http.StatusBadRequest, "número de máquinas de 1 a 10000")
	}
	if spec.Unit != nil {
		if _, err := q.GetUnit(ctx, store.GetUnitParams{ID: *spec.Unit, TenantID: spec.Tenant}); errors.Is(err, pgx.ErrNoRows) {
			return NewToken{}, errorf(http.StatusNotFound, "unidade não encontrada")
		} else if err != nil {
			return NewToken{}, err
		}
	}
	spec.CreatedBy = actor
	tok, err := CreateEnrollmentToken(ctx, q, spec)
	if err != nil {
		return tok, err
	}
	_, err = logChange(ctx, q, spec.Tenant, "enrollment_token_created", actor, nil, map[string]any{
		"token_id": tok.ID, "unit_id": spec.Unit, "max_uses": spec.Uses, "expires_at": tok.ExpiresAt, "description": spec.Description,
	})
	return tok, err
}

// LogInstallerPackage registra que o painel montou um pacote do instalador
// com um token de cadastro: o pacote leva o token, e quem o baixou fica no
// change_log como quem criou o token.
func LogInstallerPackage(ctx context.Context, q *store.Queries, tenant uuid.UUID, actor *uuid.UUID, token uuid.UUID, system string) error {
	_, err := logChange(ctx, q, tenant, "enrollment_package_downloaded", actor, nil, map[string]any{"token_id": token, "os": system})
	return err
}

// RevokeEnrollmentToken revoga um token de cadastro do tenant; as máquinas
// já cadastradas continuam. actor nulo = linha de comando.
func RevokeEnrollmentToken(ctx context.Context, q *store.Queries, tenant uuid.UUID, actor *uuid.UUID, id uuid.UUID) (store.RevokeEnrollmentTokenRow, error) {
	r, err := q.RevokeEnrollmentToken(ctx, store.RevokeEnrollmentTokenParams{ID: id, TenantID: tenant})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errorf(http.StatusNotFound, "token não encontrado neste tenant, ou já revogado")
	} else if err != nil {
		return r, err
	}
	_, err = logChange(ctx, q, tenant, "enrollment_token_revoked", actor, nil, map[string]any{"token_id": id, "uses": r.Uses, "max_uses": r.MaxUses})
	return r, err
}

// RevokeAgent revoga o agente ativo de um ativo do tenant: a próxima
// requisição dele recebe 401. Para voltar, a máquina se cadastra de novo.
func (s *Server) RevokeAgent(ctx context.Context, by store.Actor, asset uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		if _, err := assetInTenant(ctx, q, asset, by.TenantID); err != nil {
			return err
		}
		agent, err := q.GetAssetAgentToRevoke(ctx, store.GetAssetAgentToRevokeParams{AssetID: asset, TenantID: by.TenantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusConflict, "a máquina não tem agente ativo")
		} else if err != nil {
			return err
		}
		n, err := q.RevokeAssetAgents(ctx, asset)
		if err != nil {
			return err
		}
		_, err = logChange(ctx, q, by.TenantID, "agent_revoked", &by.ID, &asset, map[string]any{"agent_id": agent, "revoked_agents": n})
		return err
	})
}

// SetPassword troca a senha de um login e derruba as sessões dele. by nulo =
// linha de comando (recuperação de acesso).
func SetPassword(ctx context.Context, pool *pgxpool.Pool, email, newPassword string, by *uuid.UUID) error {
	email, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	hash, err := password.Hash(newPassword)
	if err != nil {
		return errorf(http.StatusBadRequest, "%s", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := store.New(tx)
	login, err := q.GetLoginByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return errorf(http.StatusNotFound, "nenhum login com o e-mail %s", email)
	} else if err != nil {
		return err
	}
	if err := q.SetLoginPassword(ctx, store.SetLoginPasswordParams{ID: login.ID, PasswordHash: hash}); err != nil {
		return err
	}
	if _, err := q.DeleteLoginSessions(ctx, login.ID); err != nil {
		return err
	}
	actors, err := q.ListLoginActors(ctx, &login.ID)
	if err != nil {
		return err
	}
	via := "panel"
	if by == nil {
		via = "cli"
	}
	for _, a := range actors {
		// Pelo painel, quem troca é a própria pessoa: em cada tenant, o ator dela.
		var who *uuid.UUID
		if by != nil {
			who = &a.ID
		}
		if _, err := logChange(ctx, q, a.TenantID, "password_changed", who, nil, map[string]any{"actor_id": a.ID, "via": via}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RecentAssetHistory são os eventos de um ativo do tenant do mais novo para
// o mais velho (o painel); before 0 = os últimos. A rota /v1 anda para a
// frente, por cursor (AssetHistory).
func (s *Server) RecentAssetHistory(ctx context.Context, tenant, id uuid.UUID, before int64, kinds []string, limit int32) ([]Change, error) {
	if limit < 1 || limit > maxPage {
		limit = defaultPage
	}
	if kinds == nil {
		kinds = []string{}
	}
	var out []Change
	_, err := s.readQuery(ctx, tenant, func(q *store.Queries) error {
		if err := requireAsset(ctx, q, id, tenant); err != nil {
			return err
		}
		rows, err := q.ListAssetChangesRecent(ctx, store.ListAssetChangesRecentParams{
			TenantID: tenant, AssetID: &id, BeforeID: before, Kinds: kinds, PageSize: limit,
		})
		for _, c := range rows {
			out = append(out, Change{ID: c.ID, OccurredAt: c.OccurredAt, Kind: c.Kind, AssetID: c.AssetID, ActorID: c.ActorID, Data: c.Data})
		}
		return err
	})
	return out, err
}

// TenantStatus é a situação do parque do tenant (a visão geral do painel e o
// invenire-server status).
func (s *Server) TenantStatus(ctx context.Context, tenant uuid.UUID, stale time.Duration, now time.Time) (ParkStatus, error) {
	return TenantParkStatus(ctx, s.q, tenant, stale, now)
}

// BadRequest é um erro 400 com a mensagem para quem pediu.
func BadRequest(msg string) error { return &httpError{status: http.StatusBadRequest, msg: msg} }
