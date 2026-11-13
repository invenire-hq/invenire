package web

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/download"
	"github.com/athosbes/invenire/internal/server/store"
)

// O instalador pré-configurado do agente (Fase 3), na tela do token recém-
// criado, em três formas: pacote para baixar, comando de uma linha e manual.
//
// Como o token aparece uma vez só e o banco guarda só o hash dele, o pacote
// não pode ser montado depois a partir do banco. Ele é montado na hora, a
// partir do token que a própria página mostra: o botão de baixar é um
// formulário POST que devolve o token no corpo (nunca na URL), com a sessão
// de administrador e o token CSRF. O servidor confere que o token é deste
// tenant e ainda vale (pelo hash) e monta o pacote em memória; nada é
// guardado, nem no banco nem na memória do processo. Recarregar a página
// apaga o token da tela e, com ele, a chance de baixar o pacote: para outro
// pacote, crie outro token.

// installView é o que a tela do token mostra sobre as formas de instalar.
type installView struct {
	// Problem explica por que não há pacote (servidor sem os executáveis).
	Problem string
	Windows bool
	Linux   bool
	// OneLine: o comando de uma linha só existe com certificado público,
	// porque o download precisa confiar no servidor sem pin.
	OneLine   bool
	ScriptPS1 string
	ScriptSH  string
}

func (p *Panel) installView(server, pin string) installView {
	v := installView{
		Problem: p.agents.Problem(),
		Windows: p.agents.Has("windows-amd64"),
		Linux:   p.agents.Has("linux-amd64") || p.agents.Has("linux-arm64"),
		OneLine: pin == "" && download.ValidServer(server),
	}
	if v.OneLine {
		v.ScriptPS1 = server + "/download/instalar.ps1"
		v.ScriptSH = server + "/download/instalar.sh"
	}
	return v
}

// installScript serve o instalar.ps1 ou o instalar.sh com o endereço pelo
// qual foi pedido (o mesmo que o painel mostra) e o pin, se houver.
func (p *Panel) installScript(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		download.ServeScript(w, kind, serverURL(r), p.api.CAPin())
	}
}

// enrollPackage é POST /enroll/package: o pacote do instalador para o token
// do formulário (veja o comentário do começo do arquivo).
func (p *Panel) enrollPackage(w http.ResponseWriter, r *http.Request, s *session) {
	token := strings.TrimSpace(r.PostFormValue("token"))
	system := r.PostFormValue("so")
	if system != "windows" && system != "linux" {
		p.packageError(w, r, s, msg("package.bad_os"))
		return
	}
	h := sha256.Sum256([]byte(token))
	row, err := p.q.GetEnrollmentTokenByHash(r.Context(), store.GetEnrollmentTokenByHashParams{TokenHash: h[:], TenantID: s.Actor.TenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		p.packageError(w, r, s, msg("package.token_invalid"))
		return
	} else if err != nil {
		p.fail(w, r, s, err)
		return
	}
	cfg := download.Config{
		Server: serverURL(r), Pin: p.api.CAPin(), Token: token,
		ExpiresAt: row.ExpiresAt.Local(), Remaining: int(row.MaxUses - row.Uses), Generated: p.now().Local(),
	}
	name, ctype, body, err := p.agents.Package(system, cfg)
	if err != nil {
		p.log.WarnContext(r.Context(), "pacote do instalador", "os", system, "err", err)
		p.packageError(w, r, s, msg("package.unavailable")+" ("+err.Error()+")")
		return
	}
	err = p.api.InTx(r.Context(), func(q *store.Queries) error {
		return api.LogInstallerPackage(r.Context(), q, s.Actor.TenantID, &s.Actor.ID, row.ID, system)
	})
	if err != nil {
		p.fail(w, r, s, err)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// packageError mostra o erro numa página própria: o formulário do pacote
// abre numa aba nova, e a aba com o token continua aberta atrás.
func (p *Panel) packageError(w http.ResponseWriter, r *http.Request, s *session, text string) {
	p.render(w, r, http.StatusBadRequest, "error", s, pageData{Title: msg("error.title"), Data: text})
}
