// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/invenire-hq/invenire/internal/server/api"
	"github.com/invenire-hq/invenire/internal/server/store"
	"github.com/invenire-hq/invenire/pkg/snapshot"
)

func TestMultiUseToken(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := api.CreateEnrollmentToken(ctx, e.q, api.TokenSpec{Tenant: e.tenant.ID, TTL: time.Hour, Uses: 0}); err == nil {
		t.Fatal("token com 0 usos")
	}
	tok, err := api.CreateEnrollmentToken(ctx, e.q, api.TokenSpec{Tenant: e.tenant.ID, TTL: time.Hour, Uses: 2, Description: "lote"})
	if err != nil {
		t.Fatal(err)
	}
	hosts := []snapshot.Host{
		testHost, secondHost,
		{Hostname: "TERCEIRA", SMBIOSUUID: "0A0B0C0D-1111-2222-3333-444455556666"},
	}
	for i, h := range hosts {
		status, _, _ := e.enroll(tok.Token, h)
		want := http.StatusCreated
		if i == 2 {
			want = http.StatusUnauthorized // gasto depois de dois usos
		}
		if status != want {
			t.Fatalf("cadastro %d: %d, queria %d", i+1, status, want)
		}
	}
	list, _ := e.q.ListEnrollmentTokens(ctx, store.ListEnrollmentTokensParams{TenantID: e.tenant.ID, IncludeAll: true})
	if len(list) != 1 || list[0].Uses != 2 || list[0].MaxUses != 2 || list[0].UsedAt == nil {
		t.Fatalf("token depois dos usos: %+v", list)
	}
	if valid, _ := e.q.ListEnrollmentTokens(ctx, store.ListEnrollmentTokensParams{TenantID: e.tenant.ID}); len(valid) != 0 {
		t.Fatalf("token gasto aparece como válido: %+v", valid)
	}

	// Revogado no meio: os cadastros seguintes param, os feitos continuam.
	tok2, _ := api.CreateEnrollmentToken(ctx, e.q, api.TokenSpec{Tenant: e.tenant.ID, TTL: time.Hour, Uses: 5})
	if status, _, _ := e.enroll(tok2.Token, hosts[2]); status != http.StatusCreated {
		t.Fatalf("antes de revogar: %d", status)
	}
	r, err := e.q.RevokeEnrollmentToken(ctx, store.RevokeEnrollmentTokenParams{ID: tok2.ID, TenantID: e.tenant.ID})
	if err != nil || r.Uses != 1 {
		t.Fatalf("revogar: %+v %v", r, err)
	}
	if status, _, _ := e.enroll(tok2.Token, snapshot.Host{Hostname: "QUARTA", SMBIOSUUID: "0A0B0C0D-1111-2222-3333-777788889999"}); status != http.StatusUnauthorized {
		t.Fatalf("depois de revogar: %d", status)
	}
	other, _ := e.q.CreateTenant(ctx, "outro")
	if _, err := e.q.RevokeEnrollmentToken(ctx, store.RevokeEnrollmentTokenParams{ID: tok.ID, TenantID: other.ID}); err == nil {
		t.Fatal("outro tenant revogou o token")
	}
	// Cada uso fica no change_log com o token.
	if n := count(e.kinds(e.tenant.ID), "agent_enrolled"); n != 3 {
		t.Fatalf("agent_enrolled: %d", n)
	}
}

func TestParkStatus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ubs, _, _ := api.CreateUnit(ctx, e.q, e.tenant.ID, nil, nil, "", "UBS")

	// A: em dia. B: cadastrado sem coleta. C: atrasado. D: revogado.
	_, a, certA := e.enroll(e.token(), testHost)
	e.sendSoftware(certA, a.AgentID, testHost, "", time.Now().UTC().Truncate(time.Second).Add(-time.Minute), chrome130)
	if _, err := e.pool.Exec(ctx, "UPDATE snapshot SET processed_at = now()"); err != nil {
		t.Fatal(err)
	}
	e.pool.Exec(ctx, "UPDATE asset SET unit_id = $1 WHERE id = $2", ubs.ID, a.AssetID)
	_, b, _ := e.enroll(e.token(), secondHost)
	hostC := snapshot.Host{Hostname: "C-ATRASADO", SMBIOSUUID: "0A0B0C0D-1111-2222-3333-aaaabbbbcccc"}
	_, c, certC := e.enroll(e.token(), hostC)
	e.sendSoftware(certC, c.AgentID, hostC, "", time.Now().UTC().Truncate(time.Second).Add(-48*time.Hour), chrome130)
	e.pool.Exec(ctx, "UPDATE snapshot SET processed_at = now()")
	e.pool.Exec(ctx, "UPDATE agent SET last_seen_at = now() - interval '2 days' WHERE id = $1", c.AgentID)
	hostD := snapshot.Host{Hostname: "D-REVOGADO", SMBIOSUUID: "0A0B0C0D-1111-2222-3333-ddddeeeeffff"}
	_, d, _ := e.enroll(e.token(), hostD)
	e.q.RevokeAgent(ctx, d.AgentID)
	api.CreateEnrollmentToken(ctx, e.q, api.TokenSpec{Tenant: e.tenant.ID, TTL: time.Hour, Uses: 10})

	p, err := api.TenantParkStatus(ctx, e.q, e.tenant.ID, 12*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]string{}
	for _, s := range p.Assets {
		state[s.Hostname] = s.State
	}
	want := map[string]string{
		testHost.Hostname: api.StateOK, secondHost.Hostname: api.StateNoData,
		hostC.Hostname: api.StateStale, hostD.Hostname: api.StateNoAgent,
	}
	for h, s := range want {
		if state[h] != s {
			t.Errorf("%s: %q, queria %q", h, state[h], s)
		}
	}
	if p.Assets[0].State != api.StateNoAgent || p.Problems() != 3 || p.ValidTokens != 10 || p.ByVersion["0.1.0"] != 3 {
		t.Fatalf("resumo: %+v problemas %d tokens %d versões %v", p.ByState, p.Problems(), p.ValidTokens, p.ByVersion)
	}
	for _, s := range p.Assets {
		if s.Hostname == testHost.Hostname && (s.Unit != "UBS" || s.LastCollected == nil || s.Installed != 0) {
			t.Fatalf("linha de A: %+v", s)
		}
	}
	_ = b
}
