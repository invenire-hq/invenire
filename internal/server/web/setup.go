// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/invenire-hq/invenire/internal/server/api"
)

// A primeira configuração: numa instalação sem tenant, / leva a /setup, que
// pede o código mostrado no log do serve (ou dado em INVENIRE_SETUP_CODE).
// Sem o código, quem achasse primeiro uma instalação nova exposta na
// internet tomaria posse dela. O /setup cria a organização e o primeiro
// administrador, já logado, e leva a "Adicionar máquinas" com as boas-vindas,
// que oferecem a primeira unidade e o primeiro token de cadastro.

type setupView struct {
	Org, Name, Email string
	Error            string
}

func (p *Panel) setupForm(w http.ResponseWriter, r *http.Request) {
	n, err := p.q.CountTenants(r.Context())
	if err != nil {
		p.fail(w, r, nil, err)
		return
	}
	if n > 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p.render(w, r, http.StatusOK, "setup", nil, pageData{Title: msg("setup.title"), Data: setupView{}})
}

func (p *Panel) setupSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	view := setupView{
		Org:   strings.TrimSpace(r.PostFormValue("org")),
		Name:  strings.TrimSpace(r.PostFormValue("name")),
		Email: strings.TrimSpace(r.PostFormValue("email")),
	}
	show := func(status int, text string) {
		view.Error = text
		p.render(w, r, status, "setup", nil, pageData{Title: msg("setup.title"), Data: view})
	}
	if n, err := p.q.CountTenants(r.Context()); err != nil {
		p.fail(w, r, nil, err)
		return
	} else if n > 0 {
		show(http.StatusConflict, msg("setup.already"))
		return
	}
	key := "ip:" + clientIP(r)
	if !p.setupLimit.allow(key) {
		show(http.StatusTooManyRequests, msg("login.too_many"))
		return
	}
	p.setupMu.Lock()
	code := p.setupCode
	p.setupMu.Unlock()
	got := normalizeSetupCode(r.PostFormValue("code"))
	if code == "" || subtle.ConstantTimeCompare([]byte(got), []byte(code)) != 1 {
		p.setupLimit.fail(key)
		p.log.WarnContext(r.Context(), "código de configuração errado", "ip", clientIP(r))
		show(http.StatusForbidden, msg("setup.bad_code"))
		return
	}
	pw := r.PostFormValue("password")
	if pw != r.PostFormValue("password2") {
		show(http.StatusBadRequest, msg("account.mismatch"))
		return
	}
	org, err := api.CreateOrganization(r.Context(), p.api.Pool(), api.OrgSpec{
		Name: view.Org, AdminName: view.Name, AdminEmail: view.Email, AdminPassword: pw, OnlyIfFirst: true,
	})
	if errors.Is(err, api.ErrAlreadySetUp) {
		show(http.StatusConflict, msg("setup.already"))
		return
	}
	if status, text, mine := api.ErrorMessage(err); err != nil && mine {
		show(status, text)
		return
	} else if err != nil {
		p.fail(w, r, nil, err)
		return
	}
	// O código serviu: não vale de novo, nem se a organização for apagada.
	p.setupMu.Lock()
	p.setupCode = ""
	p.setupMu.Unlock()
	p.log.InfoContext(r.Context(), "primeira configuração feita pelo painel", "tenant_id", org.Tenant.ID, "actor_id", org.Admin.ID, "ip", clientIP(r))
	if err := p.startSession(w, r, *org.Admin.LoginID, org.Admin.ID); err != nil {
		p.fail(w, r, nil, err)
		return
	}
	p.done(w, r, "/enroll?primeira=1", msg("setup.done"))
}
