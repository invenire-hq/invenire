// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/invenire-hq/invenire/internal/server/api"
	"github.com/invenire-hq/invenire/internal/server/ingest"
	"github.com/invenire-hq/invenire/internal/server/normalize"
	"github.com/invenire-hq/invenire/pkg/snapshot"
)

var (
	chrome130  = snapshot.Software{Source: "win_registry", Name: "Google Chrome", Publisher: "Google LLC", Version: "130.0.6723.70"}
	chrome131  = snapshot.Software{Source: "win_registry", Name: "Google Chrome", Publisher: "Google LLC", Version: "131.0.6778.86"}
	firefox    = snapshot.Software{Source: "win_registry", Name: "Mozilla Firefox (x64 pt-BR)", Publisher: "Mozilla", Version: "132.0", InstallPath: `C:\Program Files\Mozilla Firefox`}
	secondHost = snapshot.Host{Hostname: "ALFA-02", SMBIOSUUID: "5C1D0B7E-2F44-4C1A-9E0B-1F2A3B4C5D6E"}
)

// sendSoftware manda um snapshot com esse software, coletado em at, e
// devolve o hash para encadear o próximo.
func (e *env) sendSoftware(cert *tls.Certificate, agentID uuid.UUID, host snapshot.Host, prev string, at time.Time, sw ...snapshot.Software) string {
	e.t.Helper()
	s := newSnapshot(host, prev)
	s.CollectedAt = at
	s.AgentID = agentID.String()
	s.Software = sw
	body, sum, _ := snapshot.Marshal(s)
	if status, _, msg := e.send(e.client(cert), body, sum); status != http.StatusAccepted {
		e.t.Fatalf("envio: %d %s", status, msg)
	}
	return sum
}

func TestQueryRoutes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rules, err := normalize.Default()
	if err != nil {
		t.Fatal(err)
	}
	worker := ingest.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), rules)
	drain := func() {
		t.Helper()
		if _, err := worker.Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}

	saude, _, _ := api.CreateUnit(ctx, e.q, e.tenant.ID, nil, nil, "secretaria", "Saúde")
	ubs, _, _ := api.CreateUnit(ctx, e.q, e.tenant.ID, nil, &saude.ID, "ubs", "UBS Centro")

	// A: na UBS, com Chrome 130 e Firefox. B: sem unidade, com Chrome 131.
	tokA, _, _ := api.NewEnrollmentToken(ctx, e.q, e.tenant.ID, &ubs.ID, "", time.Hour)
	_, a, certA := e.enroll(tokA, testHost)
	_, b, certB := e.enroll(e.token(), secondHost)
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	sumA := e.sendSoftware(certA, a.AgentID, testHost, "", t0, chrome130, firefox)
	e.sendSoftware(certB, b.AgentID, secondHost, "", t0, chrome131)
	drain()

	admin := e.user(e.tenant, "admin", "admin")
	reader := e.user(e.tenant, "leitor", "read")
	other, _ := e.q.CreateTenant(ctx, "outro")
	stranger := e.user(other, "leitor-de-outro", "read")
	if status := e.call("PUT", "/v1/assets/"+a.AssetID.String()+"/labels/ambiente", admin, api.LabelRequest{Value: "produção"}, nil); status != http.StatusOK {
		t.Fatalf("etiqueta: %d", status)
	}

	assets := func(token, query string) api.AssetList {
		t.Helper()
		var l api.AssetList
		if status := e.call("GET", "/v1/assets?"+query, token, nil, &l); status != http.StatusOK {
			t.Fatalf("GET /v1/assets?%s: %d", query, status)
		}
		return l
	}
	ids := func(l api.AssetList) []uuid.UUID {
		var r []uuid.UUID
		for _, it := range l.Items {
			r = append(r, it.ID)
		}
		return r
	}
	same := func(name string, got []uuid.UUID, want ...uuid.UUID) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, queria %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, queria %v", name, got, want)
			}
		}
	}

	// Lista: em ordem de hostname (ALFA-02 antes de PMC-ARR-01).
	all := assets(reader, "")
	same("todos", ids(all), b.AssetID, a.AssetID)
	if all.AsOf == 0 || *all.Items[0].Collected.Hostname != "ALFA-02" || all.Items[1].Unit == nil ||
		all.Items[1].Unit.Path != "Saúde/UBS Centro" || all.Items[1].Labels["ambiente"] != "produção" ||
		all.Items[1].Agent == nil || all.Items[1].Agent.ID != a.AgentID || all.Items[1].InstalledCount != 2 {
		t.Fatalf("itens: %+v", all.Items)
	}
	same("unidade de cima inclui as de baixo", ids(assets(reader, "unit="+url.QueryEscape("Saúde"))), a.AssetID)
	same("só a unidade", ids(assets(reader, "unit="+saude.ID.String()+"&subtree=false")))
	same("sem unidade", ids(assets(reader, "unit=none")), b.AssetID)
	same("etiqueta", ids(assets(reader, "label=ambiente=produção")), a.AssetID)
	same("etiqueta que ninguém tem", ids(assets(reader, "label=ambiente=teste")))
	same("hostname em parte", ids(assets(reader, "hostname=arr")), a.AssetID)
	same("% não é curinga", ids(assets(reader, "hostname=%25")))
	same("outro tenant", ids(assets(stranger, "")))
	p1 := assets(reader, "limit=1")
	same("página 1", ids(p1), b.AssetID)
	same("página 2", ids(assets(reader, "limit=1&cursor="+p1.NextCursor)), a.AssetID)
	for _, bad := range []string{"cursor=xyz", "limit=0", "label=semigual", "unit=Nada"} {
		if status := e.call("GET", "/v1/assets?"+bad, reader, nil, nil); status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Errorf("%s: %d", bad, status)
		}
	}

	// Um ativo.
	var one api.AssetResponse
	if status := e.call("GET", "/v1/assets/"+a.AssetID.String(), reader, nil, &one); status != http.StatusOK || one.Asset.ID != a.AssetID {
		t.Fatalf("ativo: %d %+v", status, one)
	}
	if status := e.call("GET", "/v1/assets/"+a.AssetID.String(), stranger, nil, nil); status != http.StatusNotFound {
		t.Fatalf("ativo de outro tenant: %d", status)
	}

	// Software do ativo, em ordem de produto.
	var sw api.AssetSoftwareList
	if status := e.call("GET", "/v1/assets/"+a.AssetID.String()+"/software", reader, nil, &sw); status != http.StatusOK || len(sw.Items) != 2 {
		t.Fatalf("software do ativo: %d %+v", status, sw)
	}
	if sw.Items[0].Software.Product != "Chrome" || sw.Items[0].Collected.Version != chrome130.Version || sw.Items[0].Software.Scope != "catalog" ||
		sw.Items[1].Software.Product != "Firefox" || sw.Items[1].Collected.InstallPath != firefox.InstallPath || !sw.Items[0].InstalledSince.Equal(t0) {
		t.Fatalf("itens: %+v", sw.Items)
	}
	chromeID := sw.Items[0].Software.ID
	var swp api.AssetSoftwareList
	if e.call("GET", "/v1/assets/"+a.AssetID.String()+"/software?product=mozilla", reader, nil, &swp); len(swp.Items) != 1 || swp.Items[0].Software.Product != "Firefox" {
		t.Fatalf("filtro por produto ou fabricante: %+v", swp.Items)
	}
	if status := e.call("GET", "/v1/assets/"+a.AssetID.String()+"/software", stranger, nil, nil); status != http.StatusNotFound {
		t.Fatalf("software de ativo de outro tenant: %d", status)
	}

	// A atualiza o Chrome e remove o Firefox.
	e.sendSoftware(certA, a.AgentID, testHost, sumA, t0.Add(30*time.Minute), chrome131)
	drain()

	var hist api.ChangeList
	if status := e.call("GET", "/v1/assets/"+a.AssetID.String()+"/history", reader, nil, &hist); status != http.StatusOK {
		t.Fatalf("histórico: %d", status)
	}
	kinds := map[string]int{}
	for _, c := range hist.Items {
		if c.AssetID == nil || *c.AssetID != a.AssetID {
			t.Fatalf("evento de outro ativo: %+v", c)
		}
		kinds[c.Kind]++
	}
	if kinds["software_installed"] != 3 || kinds["software_removed"] != 2 || kinds["asset_moved"] != 1 || kinds["label_set"] != 1 || hist.More {
		t.Fatalf("histórico: %v", kinds)
	}
	var removed api.ChangeList
	e.call("GET", "/v1/assets/"+a.AssetID.String()+"/history?kind=software_removed", reader, nil, &removed)
	if len(removed.Items) != 2 {
		t.Fatalf("só remoções: %+v", removed.Items)
	}
	var future api.ChangeList
	e.call("GET", "/v1/assets/"+a.AssetID.String()+"/history?since=2099-01-01", reader, nil, &future)
	if len(future.Items) != 0 {
		t.Fatalf("since no futuro: %+v", future.Items)
	}
	if status := e.call("GET", "/v1/assets/"+a.AssetID.String()+"/history?since=ontem", reader, nil, nil); status != http.StatusBadRequest {
		t.Fatalf("since inválido: %d", status)
	}

	// Produtos do tenant e onde cada um está.
	var products api.SoftwareList
	e.call("GET", "/v1/software", reader, nil, &products)
	if len(products.Items) != 1 || products.Items[0].ID != chromeID || products.Items[0].AssetCount != 2 || products.Items[0].VersionCount != 1 {
		t.Fatalf("produtos: %+v", products.Items)
	}
	var none api.SoftwareList
	if e.call("GET", "/v1/software", stranger, nil, &none); len(none.Items) != 0 {
		t.Fatalf("produtos do outro tenant: %+v", none.Items)
	}

	inst := func(token, query string) (int, api.InstallationList) {
		t.Helper()
		var l api.InstallationList
		status := e.call("GET", "/v1/software/"+chromeID.String()+"/installations?"+query, token, nil, &l)
		return status, l
	}
	status, il := inst(reader, "")
	if status != http.StatusOK || il.Software.Product != "Chrome" || len(il.Versions) != 1 || il.Versions[0].AssetCount != 2 || len(il.Items) != 2 ||
		il.Items[0].AssetID != b.AssetID || il.Items[1].Unit == nil || il.Items[1].Unit.ID != ubs.ID {
		t.Fatalf("instalações: %d %+v", status, il)
	}
	if _, il = inst(reader, "unit="+url.QueryEscape("Saúde/UBS Centro")); len(il.Items) != 1 || il.Items[0].AssetID != a.AssetID {
		t.Fatalf("instalações na UBS: %+v", il.Items)
	}
	if _, il = inst(reader, "version=130.0.6723.70"); len(il.Items) != 0 {
		t.Fatalf("versão que ninguém tem mais: %+v", il.Items)
	}
	// O catálogo é comum, mas as instalações são do tenant.
	if status, il = inst(stranger, ""); status != http.StatusOK || len(il.Items) != 0 || len(il.Versions) != 0 {
		t.Fatalf("instalações vistas por outro tenant: %d %+v", status, il)
	}
	if status := e.call("GET", "/v1/software/"+uuid.NewString()+"/installations", reader, nil, nil); status != http.StatusNotFound {
		t.Fatalf("produto inexistente: %d", status)
	}

	// Fluxo de mudanças: acompanhar pelo cursor.
	var c1, c2, c3 api.ChangeList
	e.call("GET", "/v1/changes?limit=3", reader, nil, &c1)
	if len(c1.Items) != 3 || !c1.More || c1.AsOf < c1.Items[2].ID {
		t.Fatalf("mudanças 1: %+v", c1)
	}
	e.call("GET", "/v1/changes?limit=200&after="+c1.NextCursor, reader, nil, &c2)
	if len(c2.Items) == 0 || c2.Items[0].ID <= c1.Items[2].ID || c2.More {
		t.Fatalf("mudanças 2: %+v", c2)
	}
	e.call("GET", "/v1/changes?after="+c2.NextCursor, reader, nil, &c3)
	if len(c3.Items) != 0 || c3.NextCursor != c2.NextCursor {
		t.Fatalf("nada novo: %+v", c3)
	}
	var moved api.ChangeList
	e.call("GET", "/v1/changes?kind=asset_moved,label_set", reader, nil, &moved)
	if len(moved.Items) != 2 {
		t.Fatalf("filtro por tipo: %+v", moved.Items)
	}
	var otherChanges api.ChangeList
	if e.call("GET", "/v1/changes", stranger, nil, &otherChanges); len(otherChanges.Items) != 0 {
		t.Fatalf("mudanças do outro tenant: %+v", otherChanges.Items)
	}
	if status := e.call("GET", "/v1/changes", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("sem token: %d", status)
	}
}
